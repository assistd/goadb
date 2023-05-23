package main

import (
	"fmt"
	"io/fs"
	"path/filepath"
)

func main() {
	for {
		filepath.WalkDir("/Users/wetest/workplace/udt/go-perfdog", func(path string, d fs.DirEntry, err error) error {
			fmt.Println("--> path:", path, d.Name())
			return nil
		})
	}
}
