// Command pestrip removes DWARF debug sections (.debug_*) from a Windows
// PE image. It exists because the Tesseract Windows build ships a 101 MB
// libtesseract-5.dll of which ~98 MB is debug data; there is no binutils
// `strip` on a bare Windows box.
//
// The debug sections must be the last sections both in the header table and
// in the file (which is how GNU ld lays them out); the tool refuses anything
// else. It rewrites the section count, the image size, drops the COFF symbol
// table pointer and truncates the file.
//
//	go run ./tools/pestrip in.dll out.dll
package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: pestrip in.dll out.dll")
		os.Exit(2)
	}
	if err := strip(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "pestrip:", err)
		os.Exit(1)
	}
}

func strip(in, out string) error {
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	f, err := pe.NewFile(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("not a PE file: %w", err)
	}
	le := binary.LittleEndian
	peOff := le.Uint32(data[0x3c:])
	coffOff := peOff + 4
	optOff := coffOff + 20
	optSize := uint32(le.Uint16(data[coffOff+16:]))
	secTable := optOff + optSize
	magic := le.Uint16(data[optOff:])
	if magic != 0x20b && magic != 0x10b {
		return fmt.Errorf("unexpected optional header magic %#x", magic)
	}
	sectionAlign := le.Uint32(data[optOff+32:])

	// Partition sections; debug sections must all come last.
	firstDrop := -1
	for i, s := range f.Sections {
		isDebug := strings.HasPrefix(s.Name, ".debug") || s.Name == ".gnu_debuglink"
		if isDebug && firstDrop < 0 {
			firstDrop = i
		}
		if !isDebug && firstDrop >= 0 {
			return fmt.Errorf("section %q follows a debug section; layout not supported", s.Name)
		}
	}
	if firstDrop < 0 {
		return fmt.Errorf("no debug sections found in %s", in)
	}
	kept, dropped := f.Sections[:firstDrop], f.Sections[firstDrop:]

	newEnd := uint32(len(data))
	for _, s := range dropped {
		if s.Size > 0 && s.Offset > 0 && s.Offset < newEnd {
			newEnd = s.Offset
		}
	}
	var imageEnd uint32
	for _, s := range kept {
		if s.Offset+s.Size > newEnd {
			return fmt.Errorf("kept section %q has data after the debug sections", s.Name)
		}
		if end := s.VirtualAddress + s.VirtualSize; end > imageEnd {
			imageEnd = end
		}
	}
	sizeOfImage := (imageEnd + sectionAlign - 1) / sectionAlign * sectionAlign

	outData := make([]byte, newEnd)
	copy(outData, data[:newEnd])

	le.PutUint16(outData[coffOff+2:], uint16(len(kept)))
	le.PutUint32(outData[coffOff+8:], 0)                                       // PointerToSymbolTable
	le.PutUint32(outData[coffOff+12:], 0)                                      // NumberOfSymbols
	le.PutUint16(outData[coffOff+18:], le.Uint16(outData[coffOff+18:])|0x0200) // IMAGE_FILE_DEBUG_STRIPPED
	le.PutUint32(outData[optOff+56:], sizeOfImage)
	le.PutUint32(outData[optOff+64:], 0) // CheckSum: not verified for user-mode images
	for i := range dropped {
		hdr := secTable + uint32(firstDrop+i)*40
		for j := uint32(0); j < 40; j++ {
			outData[hdr+j] = 0
		}
	}

	// Sanity: the result must still parse and expose the same exports.
	g, err := pe.NewFile(bytes.NewReader(outData))
	if err != nil {
		return fmt.Errorf("stripped image does not parse: %w", err)
	}
	if len(g.Sections) != len(kept) {
		return fmt.Errorf("stripped image has %d sections, want %d", len(g.Sections), len(kept))
	}
	if err := os.WriteFile(out, outData, 0o755); err != nil {
		return err
	}
	fmt.Printf("%s: %d -> %d bytes (%d debug sections removed, SizeOfImage %#x)\n", out, len(data), len(outData), len(dropped), sizeOfImage)
	return nil
}
