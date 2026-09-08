package e2e

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The harness treats "postgres refused this statement" and "the call never returned" as different
// findings -- the first fails a test, the second reports NOT MEASURED. That distinction is only
// worth anything if the discriminator actually discriminates, and it is not observable from a
// passing suite: both paths are silent when nothing goes wrong.
//
// Three arms, because two would not settle it. Deadline-classified plus refusal-classified is also
// consistent with "every error is reported as a refusal", so the third arm shows a good statement
// still comes back clean.
func TestPsqlTellsARefusalApartFromNoAnswer(t *testing.T) {
	t.Run("a deadline is reported as a deadline", func(t *testing.T) {
		// One millisecond cannot cover a docker exec, so this arm cannot accidentally succeed.
		_, err := runPsql(time.Millisecond, "SELECT 1")
		if err == nil {
			t.Fatal("a 1 ms budget was somehow met, so this arm proves nothing")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("a timed-out call was not classified as a deadline, so a slow host will be "+
				"reported as a product failure: %v", err)
		}
	})

	t.Run("a refused statement is not reported as a deadline", func(t *testing.T) {
		_, err := runPsql(psqlBudget, "SELECT this_column_does_not_exist")
		if err == nil {
			t.Fatal("postgres accepted a statement it should have refused; ON_ERROR_STOP is not in effect")
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("a real refusal was misclassified as a timeout, which would silently downgrade "+
				"a leaked-pool finding to NOT MEASURED: %v", err)
		}
	})

	t.Run("a valid statement still succeeds", func(t *testing.T) {
		if _, err := runPsql(psqlBudget, "SELECT 1"); err != nil {
			t.Fatalf("the positive control failed, so the two arms above may just be reporting that "+
				"every statement errors: %v", err)
		}
	})
}
