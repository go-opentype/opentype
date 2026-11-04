// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"encoding/binary"
	"testing"
)

// withLineMetrics writes the OS/2 line-metric set into a table os2Table built.
// The offsets are OpenType 1.9.1's: sTypoAscender 68, sTypoDescender 70,
// sTypoLineGap 72, usWinAscent 74, usWinDescent 76 — all inside a version-0
// table's 78 bytes.
func withLineMetrics(b []byte, typoAsc, typoDesc, typoGap, winAsc, winDesc int) []byte {
	binary.BigEndian.PutUint16(b[68:], uint16(int16(typoAsc)))
	binary.BigEndian.PutUint16(b[70:], uint16(int16(typoDesc)))
	binary.BigEndian.PutUint16(b[72:], uint16(int16(typoGap)))
	binary.BigEndian.PutUint16(b[74:], uint16(winAsc))
	binary.BigEndian.PutUint16(b[76:], uint16(winDesc))
	return b
}

// ⛔⛔ LES DEUX DESCENDANTES ONT DES SIGNES OPPOSÉS, et c'est tout le piège de ce
// jeu de champs. OpenType 1.9.1, OS/2 usWinDescent:
//
//	the usWinDescent value treats distances below the baseline as POSITIVE values;
//	thus, usWinDescent is usually a positive value, while sTypoDescender and
//	hhea.descender are usually negative.
//
// Un décodeur qui lirait les deux avec le même assistant rendrait l'un des deux
// absurde — en pratique usWinDescent, lu comme un grand négatif. Le témoin ci-dessous
// exige explicitement le signe de chacun.
func TestOS2LineMetricsKeepTheirOppositeSigns(t *testing.T) {
	// ⛔ LES NOMBRES DIFFÈRENT DÉLIBÉRÉMENT DE CEUX DE hhea, que descFont fixe à
	// 800 / −200 / 100. Avec les mêmes valeurs des deux côtés, aucun de ces témoins
	// ne peut distinguer « a lu OS/2 » de « a lu hhea » — et TestHheaAccessorsStillReadHhea
	// s'est effectivement SAUTÉ lui-même sur une première version qui les confondait.
	os2 := withLineMetrics(os2Table(4, 400, 5, 0, useTypoMetricsBit, 500, 700),
		750, -250, 0, 1050, 300)
	f := descFont(t, headTable(1000, 0), os2, nil)

	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"TypoAscent", f.TypoAscent(), 750},
		{"TypoDescent (NÉGATIF sous la ligne de base)", f.TypoDescent(), -250},
		{"TypoLineGap", f.TypoLineGap(), 0},
		{"WinAscent", f.WinAscent(), 1050},
		{"WinDescent (POSITIF sous la ligne de base)", f.WinDescent(), 300},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, attendu %d", c.name, c.got, c.want)
		}
	}
	if !f.UseTypoMetrics() {
		t.Error("USE_TYPO_METRICS (fsSelection bit 7) est posé et doit être rapporté")
	}
}

// ⛔ LE BIT 7 N'EST PAS LE BIT 0. fsSelection bit 0 est ITALIC, déjà lu par ce
// paquet; confondre les deux ferait dire à toute fonte italique « préférez mes
// sTypo* », une erreur silencieuse qui change la mise en page de chaque document.
func TestUseTypoMetricsIsNotTheItalicBit(t *testing.T) {
	italicOnly := withLineMetrics(os2Table(4, 400, 5, 0, 1 /* ITALIC */, 500, 700),
		800, -200, 90, 1000, 250)
	f := descFont(t, headTable(1000, 0), italicOnly, nil)
	if f.UseTypoMetrics() {
		t.Error("ITALIC seul ne doit PAS être lu comme USE_TYPO_METRICS")
	}
}

// ⛔ ET SANS TABLE OS/2, ZÉRO PARTOUT — la convention que CapHeight suit déjà. Un
// faux non-zéro serait pire qu'une absence: le lecteur croirait tenir la métrique
// de la fonte.
//
// ⚠ Et false de UseTypoMetrics ne veut PAS dire « préférez usWin* »: cela veut dire
// « cette fonte ne se prononce pas ». Le bit n'existe que depuis la version 4.
func TestLineMetricsAreZeroWithoutOS2(t *testing.T) {
	f := descFont(t, headTable(1000, 0), nil, nil)
	for _, c := range []struct {
		name string
		got  int
	}{
		{"TypoAscent", f.TypoAscent()},
		{"TypoDescent", f.TypoDescent()},
		{"TypoLineGap", f.TypoLineGap()},
		{"WinAscent", f.WinAscent()},
		{"WinDescent", f.WinDescent()},
	} {
		if c.got != 0 {
			t.Errorf("%s = %d sans table OS/2, attendu 0", c.name, c.got)
		}
	}
	if f.UseTypoMetrics() {
		t.Error("sans OS/2 la fonte ne se prononce pas: UseTypoMetrics doit être false")
	}
}

// ⛔⛔ LES ANCIENS ACCESSEURS NE CHANGENT PAS DE SENS. Ascent/Descent/LineGap lisent
// hhea et le disent; leur faire renvoyer les sTypo* referait couler tout document déjà
// composé avec ce paquet — la panne la plus coûteuse qu'un ajout d'API puisse causer,
// parce qu'elle est invisible au compilateur.
//
// ⛔ CE TÉMOIN DOIT ÊTRE CAPABLE D'ÉCHOUER. Sa première version comparait deux jeux de
// valeurs IDENTIQUES et se SAUTAIT lui-même: il n'a jamais rien exercé. Les deux jeux
// diffèrent maintenant sur les trois champs, et le test les exige nommément plutôt que
// de tester une inégalité.
func TestHheaAccessorsStillReadHhea(t *testing.T) {
	os2 := withLineMetrics(os2Table(4, 400, 5, 0, useTypoMetricsBit, 500, 700),
		750, -250, 0, 1050, 300)
	f := descFont(t, headTable(1000, 0), os2, nil)
	// descFont pose hhea à 800 / −200 / 100.
	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"Ascent (hhea)", f.Ascent(), 800},
		{"Descent (hhea)", f.Descent(), -200},
		{"LineGap (hhea)", f.LineGap(), 100},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, attendu %d — cet accesseur doit rester hhea", c.name, c.got, c.want)
		}
	}
	// Et le contrôle dans l'autre sens: les deux jeux sont bien distincts ici, donc
	// l'égalité ci-dessus n'est pas une coïncidence de fixture.
	if f.Ascent() == f.TypoAscent() || f.Descent() == f.TypoDescent() || f.LineGap() == f.TypoLineGap() {
		t.Fatal("la fixture donne les mêmes valeurs aux deux jeux: ce test ne tranche plus rien")
	}
}
