package apk

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// The APK Signing Block, read far enough to name who signed the file.
//
// What is being extracted is the SHA-256 of the signer's X.509 certificate in DER form, because
// that is the same number the phone computes: Android's `Signature.toByteArray()` returns the DER
// certificate, and the DPC hashes exactly that (`AndroidInstaller.signerOf`). Producing a digest
// over anything else here — the public key, the whole signature record — would give a value that
// looks like a signer identity, compares equal to itself forever, and never matches the device's.
//
// **This is deliberately NOT signature verification.** No digest over the file's contents is
// recomputed and no signature is checked. It reads an identity out of a structure, and the server
// treats that identity as trust-on-first-registration: the first version of a package pins its
// signer, and a later version signed by anyone else is refused. The real verification happens where
// it can be enforced — the platform installer on the phone, which will not replace an installed
// package whose signer changed. Writing a verifier here would mean a second, weaker implementation
// of the check the device already makes properly.
const (
	sigBlockMagic = "APK Sig Block 42"
	sigSchemeV2ID = 0x7109871a
	sigSchemeV3ID = 0xf05368c0
	// v3.1, added for rotation-targeted signers. Read for the same reason v3 is: it is the block a
	// file may carry INSTEAD of a plain v3 one, and a reader that knew only v2 and v3 would report
	// such an APK as unsigned.
	sigSchemeV31ID = 0x1b93ad61

	eocdSignature  = 0x06054b50
	eocdMinLen     = 22
	eocdMaxComment = 0xFFFF
)

// ErrNoSigningBlock means the file has no v2/v3 APK Signing Block.
//
// On this project that is a refusal rather than a fallback. minSdk is 29, where v2 is mandatory for
// anything the platform will install, and the only artifacts without one are APKs signed with the
// v1 JAR scheme alone — which Android 11 and later refuse outright. Reading the v1 certificate out
// of META-INF instead would let the catalog accept and pin a signer for a file no managed phone can
// install, and the failure would surface much later, on a child's device.
var ErrNoSigningBlock = fmt.Errorf("%w: it carries no APK signing block (v2/v3). An application this server manages must be v2-signed or better, because the signer is what pins the package", ErrNotAnAPK)

// signerDigest returns the lowercase hex SHA-256 of the first signer's certificate.
func signerDigest(r io.ReaderAt, size int64) (string, error) {
	cdOffset, err := centralDirectoryOffset(r, size)
	if err != nil {
		return "", err
	}
	block, err := readSigningBlock(r, cdOffset)
	if err != nil {
		return "", err
	}
	// v3.1 first, then v3, then v2: a file carrying several describes the same signing identity in
	// each, and the newest is the one the platform prefers. They are read in that order so that a
	// rotated key is reported as the platform would see it rather than as its predecessor.
	for _, id := range []uint32{sigSchemeV31ID, sigSchemeV3ID, sigSchemeV2ID} {
		value, ok := block[id]
		if !ok {
			continue
		}
		cert, err := firstCertificate(value)
		if err != nil {
			return "", fmt.Errorf("signing block 0x%08x: %w", id, err)
		}
		sum := sha256.Sum256(cert)
		return hex.EncodeToString(sum[:]), nil
	}
	return "", ErrNoSigningBlock
}

// centralDirectoryOffset finds where the zip central directory starts.
//
// archive/zip does this internally and does not expose it, and the offset is what locates the
// signing block — it sits immediately before the central directory, outside anything the zip reader
// will show. So the End Of Central Directory record is scanned for here.
func centralDirectoryOffset(r io.ReaderAt, size int64) (int64, error) {
	if size < eocdMinLen {
		return 0, fmt.Errorf("%w: file is %d bytes", ErrNoSigningBlock, size)
	}
	// Scanned backwards from the end over the largest a zip comment may be. Backwards, because the
	// four-byte EOCD signature can legitimately occur inside a comment, and the LAST occurrence
	// that yields a self-consistent record is the real one.
	span := int64(eocdMinLen + eocdMaxComment)
	if span > size {
		span = size
	}
	buf := make([]byte, span)
	if _, err := r.ReadAt(buf, size-span); err != nil {
		return 0, fmt.Errorf("read end of file: %w", err)
	}
	for i := len(buf) - eocdMinLen; i >= 0; i-- {
		if binary.LittleEndian.Uint32(buf[i:]) != eocdSignature {
			continue
		}
		commentLen := int(binary.LittleEndian.Uint16(buf[i+20:]))
		if i+eocdMinLen+commentLen != len(buf) {
			continue
		}
		offset := int64(binary.LittleEndian.Uint32(buf[i+16:]))
		if offset < 0 || offset > size {
			continue
		}
		return offset, nil
	}
	return 0, fmt.Errorf("%w: no end-of-central-directory record", ErrNoSigningBlock)
}

// readSigningBlock returns the block's id-value pairs.
//
// Layout, immediately before the central directory:
//
//	u64 sizeOfBlock         (not counting this field)
//	{ u64 length; u32 id; value[length-4] }...
//	u64 sizeOfBlock         (the same value again)
//	"APK Sig Block 42"      (16 bytes)
//
// The size appears twice so the block can be found from either end. Both copies are compared: a
// mismatch means the file was edited by something that did not understand the block, and an
// identity read out of it would be describing bytes that are no longer there.
func readSigningBlock(r io.ReaderAt, cdOffset int64) (map[uint32][]byte, error) {
	const footerLen = 24
	if cdOffset < footerLen {
		return nil, ErrNoSigningBlock
	}
	footer := make([]byte, footerLen)
	if _, err := r.ReadAt(footer, cdOffset-footerLen); err != nil {
		return nil, fmt.Errorf("read signing block footer: %w", err)
	}
	if !bytes.Equal(footer[8:], []byte(sigBlockMagic)) {
		return nil, ErrNoSigningBlock
	}
	blockSize := binary.LittleEndian.Uint64(footer)
	// 8 for the trailing size field itself, plus the magic, plus at least one pair header.
	if blockSize < footerLen || int64(blockSize) > cdOffset-8 {
		return nil, fmt.Errorf("%w: block declares %d bytes before a directory at %d", ErrNoSigningBlock, blockSize, cdOffset)
	}
	start := cdOffset - int64(blockSize) - 8
	whole := make([]byte, blockSize+8)
	if _, err := r.ReadAt(whole, start); err != nil {
		return nil, fmt.Errorf("read signing block: %w", err)
	}
	if leading := binary.LittleEndian.Uint64(whole); leading != blockSize {
		return nil, fmt.Errorf("%w: the block's two size fields disagree (%d and %d)", ErrNoSigningBlock, leading, blockSize)
	}

	pairs := whole[8 : len(whole)-footerLen]
	out := map[uint32][]byte{}
	for len(pairs) > 0 {
		if len(pairs) < 12 {
			return nil, fmt.Errorf("%w: %d trailing bytes are not a pair", ErrNoSigningBlock, len(pairs))
		}
		length := binary.LittleEndian.Uint64(pairs)
		if length < 4 || length > uint64(len(pairs)-8) {
			return nil, fmt.Errorf("%w: a pair declares %d bytes and %d remain", ErrNoSigningBlock, length, len(pairs)-8)
		}
		id := binary.LittleEndian.Uint32(pairs[8:])
		out[id] = pairs[12 : 8+length]
		pairs = pairs[8+length:]
	}
	return out, nil
}

// firstCertificate walks a v2/v3 scheme block to the first signer's first certificate.
//
// The nesting is uniform — every level is a u32 byte length followed by that many bytes — so this
// is four unwraps rather than four formats:
//
//	signers -> signer -> signed data -> certificates -> certificate
//
// v3 puts minSdk/maxSdk after the certificates inside the signed data; both are ignored, because
// this stops at the certificates and never reads past them.
func firstCertificate(block []byte) ([]byte, error) {
	signers, err := lengthPrefixed(block, "signers")
	if err != nil {
		return nil, err
	}
	signer, _, err := next(signers, "signer")
	if err != nil {
		return nil, err
	}
	signedData, _, err := next(signer, "signed data")
	if err != nil {
		return nil, err
	}
	// digests come first inside the signed data and are stepped over by their own length.
	_, rest, err := next(signedData, "digests")
	if err != nil {
		return nil, err
	}
	certificates, _, err := next(rest, "certificates")
	if err != nil {
		return nil, err
	}
	cert, _, err := next(certificates, "certificate")
	if err != nil {
		return nil, err
	}
	if len(cert) == 0 {
		return nil, errors.New("the first signer's certificate is empty")
	}
	return cert, nil
}

// lengthPrefixed unwraps one u32-length-prefixed sequence that occupies the whole input.
func lengthPrefixed(b []byte, what string) ([]byte, error) {
	v, rest, err := next(b, what)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("%s: %d bytes trail the sequence", what, len(rest))
	}
	return v, nil
}

// next reads one u32-length-prefixed element and returns it with whatever follows.
func next(b []byte, what string) (value, rest []byte, err error) {
	if len(b) < 4 {
		return nil, nil, fmt.Errorf("%s: %d bytes left, need a 4-byte length", what, len(b))
	}
	n := binary.LittleEndian.Uint32(b)
	if uint64(n) > uint64(len(b)-4) {
		return nil, nil, fmt.Errorf("%s: declares %d bytes and %d remain", what, n, len(b)-4)
	}
	return b[4 : 4+n], b[4+n:], nil
}
