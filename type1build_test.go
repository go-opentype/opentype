package opentype

import (
	"bytes"
	"fmt"
)

// eexecEncrypt is the inverse of the cipher a Type 1 program is written in,
// which is what a test needs to build one.
func eexecEncrypt(plain []byte, key uint16, lead int) []byte {
	r := key
	buf := append(bytes.Repeat([]byte{'X'}, lead), plain...)
	out := make([]byte, 0, len(buf))
	for _, p := range buf {
		c := p ^ byte(r>>8)
		r = (uint16(c)+r)*eexecC1 + eexecC2
		out = append(out, c)
	}
	return out
}

// t1num encodes numbers the way a Type 1 charstring carries them.
func t1num(vs ...int) []byte {
	var out []byte
	for _, v := range vs {
		switch {
		case v >= -107 && v <= 107:
			out = append(out, byte(v+139))
		case v >= 108 && v <= 1131:
			v -= 108
			out = append(out, byte(v/256+247), byte(v%256))
		case v <= -108 && v >= -1131:
			v = -v - 108
			out = append(out, byte(v/256+251), byte(v%256))
		default:
			out = append(out, 255, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
		}
	}
	return out
}

// t1Box is a charstring drawing a square: a side bearing and width, a move,
// three lines and a close.
func t1Box(width int) []byte {
	cs := append(t1num(0, width), t1HSbw)
	cs = append(cs, t1num(50, 50)...)
	cs = append(cs, t1RMoveTo)
	cs = append(cs, t1num(100)...)
	cs = append(cs, t1HLineTo)
	cs = append(cs, t1num(100)...)
	cs = append(cs, t1VLineTo)
	cs = append(cs, t1num(-100)...)
	cs = append(cs, t1HLineTo)
	cs = append(cs, t1ClosePath, t1EndChar)
	return cs
}

// t1Options configures the synthetic Type 1 builder.
type t1Options struct {
	glyphs     map[string][]byte
	order      []string
	subrs      [][]byte
	encoding   map[byte]string // nil means the program says StandardEncoding
	lenIV      int
	matrix     string // when set, the /FontMatrix line to write
	container  string // "", "pfb" or "pfa"
	noEexec    bool
	noCharStrs bool
	rd         string // the operator that introduces binary, "RD" unless set
}

// buildType1 assembles a complete Type 1 program.
func buildType1(o t1Options) []byte {
	lenIV := o.lenIV
	rd := o.rd
	if rd == "" {
		rd = "RD"
	}
	matrix := o.matrix
	if matrix == "" {
		matrix = "/FontMatrix [0.001 0 0 0.001 0 0] readonly def"
	}
	var clear bytes.Buffer
	clear.WriteString("%!PS-AdobeFont-1.0: Synthetic 001.001\n")
	clear.WriteString("/FontName /Synthetic def\n")
	clear.WriteString(matrix + "\n")
	if o.encoding == nil {
		clear.WriteString("/Encoding StandardEncoding def\n")
	} else {
		clear.WriteString("/Encoding 256 array\n0 1 255 {1 index exch /.notdef put} for\n")
		for code := 0; code < 256; code++ {
			if name, ok := o.encoding[byte(code)]; ok {
				fmt.Fprintf(&clear, "dup %d /%s put\n", code, name)
			}
		}
		clear.WriteString("readonly def\n")
	}
	clear.WriteString("currentdict end\ncurrentfile eexec\n")

	var priv bytes.Buffer
	priv.WriteString("XXXX dup /Private 8 dict dup begin\n")
	fmt.Fprintf(&priv, "/lenIV %d def\n", lenIV)
	if len(o.subrs) > 0 {
		fmt.Fprintf(&priv, "/Subrs %d array\n", len(o.subrs))
		for i, sub := range o.subrs {
			enc := eexecEncrypt(sub, charstringR, lenIV)
			fmt.Fprintf(&priv, "dup %d %d %s ", i, len(enc), rd)
			priv.Write(enc)
			priv.WriteString(" NP\n")
		}
		priv.WriteString("ND\n")
	}
	if !o.noCharStrs {
		fmt.Fprintf(&priv, "/CharStrings %d dict dup begin\n", len(o.glyphs))
		for _, name := range o.order {
			enc := eexecEncrypt(o.glyphs[name], charstringR, lenIV)
			fmt.Fprintf(&priv, "/%s %d %s ", name, len(enc), rd)
			priv.Write(enc)
			priv.WriteString(" ND\n")
		}
		priv.WriteString("end\nend\nmark currentfile closefile\n")
	}

	if o.noEexec {
		return append(clear.Bytes()[:len(clear.Bytes())-len("currentfile eexec\n")], priv.Bytes()...)
	}
	body := eexecEncrypt(priv.Bytes(), eexecKey, 4)
	switch o.container {
	case "pfa":
		var out bytes.Buffer
		out.Write(clear.Bytes())
		for i, b := range body {
			fmt.Fprintf(&out, "%02x", b)
			if i%32 == 31 {
				out.WriteByte('\n')
			}
		}
		out.WriteString("\n")
		return out.Bytes()
	case "pfb":
		var out bytes.Buffer
		seg := func(kind byte, data []byte) {
			n := len(data)
			out.Write([]byte{0x80, kind, byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)})
			out.Write(data)
		}
		seg(1, clear.Bytes())
		seg(2, body)
		out.Write([]byte{0x80, 3})
		return out.Bytes()
	}
	return append(clear.Bytes(), body...)
}

// simpleType1 is the fixture most tests want: .notdef, A and B.
func simpleType1(o t1Options) []byte {
	if o.glyphs == nil {
		o.glyphs = map[string][]byte{
			".notdef": append(t1num(0, 250), t1HSbw, t1EndChar),
			"A":       t1Box(600),
			"B":       t1Box(700),
		}
		o.order = []string{".notdef", "A", "B"}
	}
	return buildType1(o)
}
