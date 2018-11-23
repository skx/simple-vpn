// Package config contains some simple handling of a configuration-file
// which is used by the server.
package config

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

// Reader contains the values we've read from the configuration-file.
type Reader struct {
	// Settings contains the key-value pairs from the named file
	Settings map[string]string
}

// New opens the given file, and returns a reader-structure with
// the specified contents.
func New(filename string) (*Reader, error) {
	r := &Reader{}
	r.Settings = make(map[string]string)

	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// regexp to get our key=value lines
	keyVal := regexp.MustCompile("^([^=]+)\\s*=\\s*(.*)$")

	// read line by line
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {

		// Get the line
		line := scanner.Text()

		// Skip comments.
		if strings.HasPrefix(line, "#") {
			continue
		}

		//Get the key=value parts
		match := keyVal.FindStringSubmatch(line)
		if len(match) == 3 {

			// If we did save them
			key := match[1]
			val := match[2]

			// stripping leading & trailing whitespace
			key = strings.TrimSpace(key)
			val = strings.TrimSpace(val)

			r.Settings[key] = val
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// All done
	return r, nil
}

// Get returns the value of the given configuration key, if any.
func (r *Reader) Get(name string) string {
	return (r.Settings[name])
}

// GetWithDefault returns the value of the given configuration key, if
// it is present, otherwise it returns the supplied default value.
func (r *Reader) GetWithDefault(name string, value string) string {
	x := r.Settings[name]
	if x == "" {
		x = value
	}

	return x
}
