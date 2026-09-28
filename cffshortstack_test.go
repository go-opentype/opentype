package opentype

import "testing"

// TestShortStackOperators feeds every Type 2 path operator an operand stack
// too short for it.
//
// A charstring in a real subset font — pulled out of a PDF, then mutated by a
// fuzzer — reached rcurveline with nothing on the stack at all, and reading
// the trailing pair that was not there brought the process down. Every one of
// these operators reads its operands out of a stack the font chose the length
// of, so every one of them is asked here for a curve it has not been given.
//
// Drawing nothing is the right answer: a malformed charstring says nothing
// about where the pen should go, and the glyph that comes back is the part
// that could be read.
func TestShortStackOperators(t *testing.T) {
	// Each operator, by its Type 2 byte, at every stack length below the one
	// it needs. The two the defect was in need eight operands to be
	// well-formed, so every length up to eight is tried.
	ops := []struct {
		name string
		b0   byte
	}{
		{"rlineto", 5}, {"hlineto", 6}, {"vlineto", 7}, {"rrcurveto", 8},
		{"rcurveline", 24}, {"rlinecurve", 25}, {"vvcurveto", 26},
		{"hhcurveto", 27}, {"vhcurveto", 30}, {"hvcurveto", 31},
	}
	for _, op := range ops {
		for n := 0; n <= 8; n++ {
			m := &t2machine{c: &cffTable{}}
			for k := 0; k < n; k++ {
				if err := m.push(float64(10 * (k + 1))); err != nil {
					t.Fatalf("%s: pushing %d operands: %v", op.name, n, err)
				}
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s with %d operands panicked: %v", op.name, n, r)
					}
				}()
				// The return values do not matter: an operator that refuses
				// its operands is fine, and an operator that draws what it
				// can is fine. Reading past the stack is not.
				_, _, _ = m.operator(op.b0, nil, 0, 0)
			}()
		}
	}
}

// TestShortStackKeepsDrawingWhatItCan checks the guards did not turn a
// well-formed charstring into a silent no-op: the operand counts the
// specification gives must still draw.
func TestShortStackKeepsDrawingWhatItCan(t *testing.T) {
	cases := []struct {
		name  string
		b0    byte
		count int
	}{
		// rcurveline is 6n+2 operands, rlinecurve is 2n+6.
		{"rcurveline", 24, 8}, {"rcurveline", 24, 14},
		{"rlinecurve", 25, 8}, {"rlinecurve", 25, 10},
	}
	for _, c := range cases {
		m := &t2machine{c: &cffTable{}}
		for k := 0; k < c.count; k++ {
			if err := m.push(float64(10 * (k + 1))); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
		}
		if _, _, err := m.operator(c.b0, nil, 0, 0); err != nil {
			t.Fatalf("%s with %d operands: %v", c.name, c.count, err)
		}
		if m.x == 0 && m.y == 0 {
			t.Errorf("%s with %d operands moved the pen nowhere", c.name, c.count)
		}
	}
}
