package apk

import (
	"encoding/binary"
	"fmt"
	"unicode/utf16"
)

// Android's binary-XML ("AXML") reader, cut down to what a manifest has to answer.
//
// Written here rather than taken from a library on purpose. The whole dependency list of this
// server is five direct modules, every one of them load-bearing, and the alternatives for this job
// are either a full APK-verification framework (which brings a signature-verification
// implementation this code does not want to inherit) or an AXML decoder that does not read the
// signing block at all — so one of the two halves would have to be written anyway. What is needed
// is narrow: three attributes off the root element and one off <uses-sdk>. Everything else in the
// format is skipped by length.
//
// The format is a sequence of length-prefixed chunks. That is the property this reader leans on:
// an unknown or malformed chunk is stepped over by its own declared size, so a manifest carrying
// something this code has never seen parses rather than failing, and a manifest whose sizes do not
// add up fails rather than reading past its own end.
const (
	chunkStringPool  = 0x0001
	chunkXML         = 0x0003
	chunkStartElem   = 0x0102
	stringPoolUTF8   = 1 << 8
	resTypeString    = 0x03
	resTypeIntDec    = 0x10
	resTypeIntHex    = 0x11
	androidNamespace = "http://schemas.android.com/apk/res/android"
)

// ErrNotBinaryXML is returned for a file that is not an Android binary manifest at all — a plain
// text AndroidManifest.xml, for instance, which is what an APK built by hand usually carries.
var ErrNotBinaryXML = fmt.Errorf("%w: its manifest is not Android binary XML", ErrNotAnAPK)

// manifestAttributes is what this reader is for: the handful of values the catalog needs.
//
// Pointers are deliberate for the numbers. A missing android:versionCode and a versionCode of 0 are
// different states — the first is a manifest this code could not read, the second is a real (if
// odd) version — and collapsing them would let a parse failure register in the catalog as a
// legitimate app whose every later version is "newer".
type manifestAttributes struct {
	pkg              string
	versionCode      *uint32
	versionCodeMajor *uint32
	versionName      string
	minSDK           *uint32
	label            string
}

// parseManifest reads an AndroidManifest.xml in Android's binary form.
func parseManifest(data []byte) (*manifestAttributes, error) {
	r := &reader{buf: data}
	typ, fileHeaderSize, size, err := r.chunkHeader()
	if err != nil {
		return nil, ErrNotBinaryXML
	}
	if typ != chunkXML {
		return nil, ErrNotBinaryXML
	}
	// The file chunk's size covers the whole document. A size that overruns the file is the
	// signature of a truncated download, and it is worth naming as such rather than surfacing as a
	// missing attribute two hundred lines later.
	if int(size) > len(data) {
		return nil, fmt.Errorf("%w: declares %d bytes and the file has %d", ErrNotBinaryXML, size, len(data))
	}
	// The document's children begin after the file chunk's own header. Starting the walk at 0
	// instead reads the file chunk again, whose declared size is the whole document — so the very
	// first step lands past the end and the loop exits having parsed nothing, which surfaces as
	// "no <manifest> element" on a perfectly good APK.
	if fileHeaderSize < 8 || int(fileHeaderSize) > len(data) {
		return nil, fmt.Errorf("%w: file header size %d", ErrNotBinaryXML, fileHeaderSize)
	}
	r.pos = int(fileHeaderSize)

	var pool []string
	out := &manifestAttributes{}
	seenManifest := false

	for r.remaining() >= 8 {
		start := r.pos
		typ, headerSize, size, err := r.chunkHeader()
		if err != nil {
			return nil, err
		}
		if size < 8 || int(size) > len(data)-start {
			return nil, fmt.Errorf("%w: chunk 0x%04x at %d declares %d bytes", ErrNotBinaryXML, typ, start, size)
		}
		body := data[start:][:size]

		switch typ {
		case chunkStringPool:
			pool, err = parseStringPool(body)
			if err != nil {
				return nil, err
			}
		case chunkStartElem:
			name, attrs, err := parseStartElement(body, int(headerSize), pool)
			if err != nil {
				return nil, err
			}
			switch name {
			case "manifest":
				seenManifest = true
				out.pkg = attrs.str("", "package")
				out.versionCode = attrs.u32(androidNamespace, "versionCode")
				out.versionCodeMajor = attrs.u32(androidNamespace, "versionCodeMajor")
				out.versionName = attrs.str(androidNamespace, "versionName")
			case "uses-sdk":
				out.minSDK = attrs.u32(androidNamespace, "minSdkVersion")
			case "application":
				// Only when it is a literal. A label is usually @string/app_name, which resolves
				// through resources.arsc — a whole second format, parsed to obtain a display string
				// that the console can perfectly well take from the operator. An unresolved
				// reference stays empty rather than becoming "@2131034112".
				out.label = attrs.str(androidNamespace, "label")
			}
		}
		// Always by the chunk's own declared size, never by how far the branch above happened to
		// read. This is what makes an unrecognised chunk harmless.
		r.pos = start + int(size)
	}
	if !seenManifest {
		return nil, fmt.Errorf("%w: no <manifest> element", ErrNotBinaryXML)
	}
	return out, nil
}

// attribute is one parsed XML attribute, kept in the form the manifest stored it.
type attribute struct {
	namespace string
	name      string
	typ       uint8
	data      uint32
	raw       string
}

type attributes []attribute

func (a attributes) find(namespace, name string) *attribute {
	for i := range a {
		if a[i].namespace == namespace && a[i].name == name {
			return &a[i]
		}
	}
	return nil
}

func (a attributes) str(namespace, name string) string {
	at := a.find(namespace, name)
	if at == nil {
		return ""
	}
	return at.raw
}

// u32 reads an integer attribute, and refuses to read one that is not stored as an integer.
//
// aapt2 writes android:versionCode as TYPE_INT_DEC, but a manifest can legally carry a resource
// reference there instead, and a reference's `data` is a resource id — a large, meaningless number
// that would sail into the catalog as a version. Returning nil for anything that is not a literal
// integer makes that case a refusal instead.
func (a attributes) u32(namespace, name string) *uint32 {
	at := a.find(namespace, name)
	if at == nil {
		return nil
	}
	if at.typ != resTypeIntDec && at.typ != resTypeIntHex {
		return nil
	}
	v := at.data
	return &v
}

func parseStartElement(body []byte, headerSize int, pool []string) (string, attributes, error) {
	// ResXMLTree_node (lineNumber, comment) is inside the header; ResXMLTree_attrExt follows it.
	if headerSize < 8 || len(body) < headerSize+20 {
		return "", nil, fmt.Errorf("%w: start element is shorter than its own header", ErrNotBinaryXML)
	}
	ext := body[headerSize:]
	nameIdx := binary.LittleEndian.Uint32(ext[4:])
	attrStart := int(binary.LittleEndian.Uint16(ext[8:]))
	attrSize := int(binary.LittleEndian.Uint16(ext[10:]))
	attrCount := int(binary.LittleEndian.Uint16(ext[12:]))

	name := poolAt(pool, nameIdx)
	if attrSize < 20 {
		// 20 bytes is the smallest an attribute record has ever been (ns, name, rawValue, ResValue).
		// A smaller stride would make the loop below read the same bytes repeatedly.
		return name, nil, fmt.Errorf("%w: attribute stride %d is too small", ErrNotBinaryXML, attrSize)
	}
	out := make(attributes, 0, attrCount)
	for i := 0; i < attrCount; i++ {
		off := headerSize + attrStart + i*attrSize
		if off+20 > len(body) {
			return name, nil, fmt.Errorf("%w: attribute %d of <%s> runs past the chunk", ErrNotBinaryXML, i, name)
		}
		rec := body[off:]
		at := attribute{
			namespace: poolAt(pool, binary.LittleEndian.Uint32(rec[0:])),
			name:      poolAt(pool, binary.LittleEndian.Uint32(rec[4:])),
			// ResXMLTree_attribute is ns, name, rawValue (4 bytes each) then Res_value
			// {u16 size, u8 res0, u8 dataType, u32 data} — so the type byte is at 15 and the
			// payload at 16, not at the end of the record.
			typ:  rec[15],
			data: binary.LittleEndian.Uint32(rec[16:]),
		}
		if rawIdx := binary.LittleEndian.Uint32(rec[8:]); rawIdx != 0xFFFFFFFF {
			at.raw = poolAt(pool, rawIdx)
		} else if at.typ == resTypeString {
			at.raw = poolAt(pool, at.data)
		}
		out = append(out, at)
	}
	return name, out, nil
}

func poolAt(pool []string, i uint32) string {
	if i == 0xFFFFFFFF || int(i) >= len(pool) {
		return ""
	}
	return pool[i]
}

// parseStringPool reads the document's string table.
//
// Both encodings are handled because both occur: aapt2 emits UTF-8 pools, older tooling and some
// repackagers emit UTF-16. A reader that assumed one would return mojibake rather than an error for
// the other — a package name of Chinese characters where "io.github…" should be — and mojibake
// reaches the database as a real-looking row.
func parseStringPool(body []byte) ([]string, error) {
	if len(body) < 28 {
		return nil, fmt.Errorf("%w: string pool shorter than its header", ErrNotBinaryXML)
	}
	count := int(binary.LittleEndian.Uint32(body[8:]))
	flags := binary.LittleEndian.Uint32(body[16:])
	stringsStart := int(binary.LittleEndian.Uint32(body[20:]))
	utf8Pool := flags&stringPoolUTF8 != 0

	if count < 0 || 28+count*4 > len(body) || stringsStart > len(body) {
		return nil, fmt.Errorf("%w: string pool declares %d strings and does not have room for them", ErrNotBinaryXML, count)
	}
	out := make([]string, count)
	for i := 0; i < count; i++ {
		off := stringsStart + int(binary.LittleEndian.Uint32(body[28+i*4:]))
		if off < 0 || off >= len(body) {
			return nil, fmt.Errorf("%w: string %d points outside the pool", ErrNotBinaryXML, i)
		}
		s, err := readPoolString(body[off:], utf8Pool)
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

func readPoolString(b []byte, isUTF8 bool) (string, error) {
	if isUTF8 {
		// Two lengths: the character count, then the byte count. Each is one byte, or two with the
		// high bit of the first set. Only the byte count is used — the character count is the one
		// that lies for astral-plane text.
		_, n, err := readUTF8Len(b)
		if err != nil {
			return "", err
		}
		b = b[n:]
		byteLen, n, err := readUTF8Len(b)
		if err != nil {
			return "", err
		}
		b = b[n:]
		if byteLen > len(b) {
			return "", fmt.Errorf("%w: UTF-8 string declares %d bytes and %d remain", ErrNotBinaryXML, byteLen, len(b))
		}
		return string(b[:byteLen]), nil
	}
	if len(b) < 2 {
		return "", fmt.Errorf("%w: UTF-16 string has no length", ErrNotBinaryXML)
	}
	n := int(binary.LittleEndian.Uint16(b))
	b = b[2:]
	if n&0x8000 != 0 {
		if len(b) < 2 {
			return "", fmt.Errorf("%w: UTF-16 string has a truncated long length", ErrNotBinaryXML)
		}
		n = (n&0x7FFF)<<16 | int(binary.LittleEndian.Uint16(b))
		b = b[2:]
	}
	if n*2 > len(b) {
		return "", fmt.Errorf("%w: UTF-16 string declares %d units and %d bytes remain", ErrNotBinaryXML, n, len(b))
	}
	units := make([]uint16, n)
	for i := 0; i < n; i++ {
		units[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(units)), nil
}

func readUTF8Len(b []byte) (value, consumed int, err error) {
	if len(b) < 1 {
		return 0, 0, fmt.Errorf("%w: UTF-8 string has no length", ErrNotBinaryXML)
	}
	if b[0]&0x80 == 0 {
		return int(b[0]), 1, nil
	}
	if len(b) < 2 {
		return 0, 0, fmt.Errorf("%w: UTF-8 string has a truncated long length", ErrNotBinaryXML)
	}
	return int(b[0]&0x7F)<<8 | int(b[1]), 2, nil
}

// reader is a bounds-checked cursor over the document.
type reader struct {
	buf []byte
	pos int
}

func (r *reader) remaining() int { return len(r.buf) - r.pos }

func (r *reader) chunkHeader() (typ, headerSize uint16, size uint32, err error) {
	if r.remaining() < 8 {
		return 0, 0, 0, fmt.Errorf("%w: truncated chunk header at %d", ErrNotBinaryXML, r.pos)
	}
	b := r.buf[r.pos:]
	return binary.LittleEndian.Uint16(b),
		binary.LittleEndian.Uint16(b[2:]),
		binary.LittleEndian.Uint32(b[4:]),
		nil
}
