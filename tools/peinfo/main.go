// Temporary analysis tool: compute the DLL import closure of a Windows
// executable within one directory, and show section sizes of the largest
// files so we can see what is debug data.
//
//	go run ./tools/peinfo <dir> <root.exe>
package main

import (
	"debug/pe"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	dir, root := os.Args[1], os.Args[2]
	seen := map[string]bool{}
	var order []string
	var missing []string
	var walk func(name string)
	walk = func(name string) {
		lower := strings.ToLower(name)
		if seen[lower] {
			return
		}
		seen[lower] = true
		path := filepath.Join(dir, name)
		f, err := pe.Open(path)
		if err != nil {
			// Not in the folder: a system DLL (kernel32, msvcrt, ...).
			missing = append(missing, name)
			return
		}
		order = append(order, name)
		// debug/pe's ImportedLibraries is a stub; derive DLLs from symbols
		// ("Symbol:DLL.dll").
		syms, _ := f.ImportedSymbols()
		f.Close()
		libs := map[string]bool{}
		for _, s := range syms {
			if i := strings.LastIndex(s, ":"); i >= 0 {
				libs[s[i+1:]] = true
			}
		}
		for l := range libs {
			walk(l)
		}
	}
	walk(root)

	var total int64
	fmt.Println("=== files needed (found in folder) ===")
	sort.Strings(order)
	for _, n := range order {
		st, _ := os.Stat(filepath.Join(dir, n))
		total += st.Size()
		fmt.Printf("%12s  %s\n", commas(st.Size()), n)
	}
	fmt.Printf("%12s  TOTAL\n", commas(total))
	sort.Strings(missing)
	fmt.Println("=== resolved from system (not bundled) ===")
	fmt.Println("   ", strings.Join(missing, ", "))

	fmt.Println("=== sections of the largest needed files ===")
	for _, n := range order {
		st, _ := os.Stat(filepath.Join(dir, n))
		if st.Size() < 2_000_000 {
			continue
		}
		f, err := pe.Open(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		fmt.Printf("-- %s\n", n)
		var dbg int64
		for _, s := range f.Sections {
			flag := ""
			if strings.HasPrefix(s.Name, ".debug") || strings.HasPrefix(s.Name, "/") {
				flag = "  <- debug"
				dbg += int64(s.Size)
			}
			fmt.Printf("   %-12s %12s%s\n", s.Name, commas(int64(s.Size)), flag)
		}
		fmt.Printf("   debug total: %s of %s\n", commas(dbg), commas(st.Size()))
		f.Close()
	}
}

func commas(n int64) string {
	s := fmt.Sprintf("%d", n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
