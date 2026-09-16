package service

import (
	"bytes"
	"encoding/xml"
	"os"
	"strings"
)

// InstalledCommand is the program and arguments the registered unit starts.
//
// Read from the unit file rather than from the running process, because the one
// time this matters is when nothing is running: an owner reinstalling a service
// to change something has to be offered what the service currently uses, and a
// form that offers an empty field instead is a form that quietly proposes the
// defaults. That is not a hypothetical — an empty database path reinstalled a
// node onto a fresh database, which gave the machine a new identity and dropped
// every pairing it had, with nothing on screen saying so.
//
// Absent is not an error. A unit that is not installed, or one written by
// something other than this code, yields nothing and the caller keeps whatever
// it would have done without this.
func (m Manager) InstalledCommand() (string, []string) {
	path, err := m.UnitPath()
	if err != nil {
		return "", nil
	}
	// #nosec G304 -- the path is this manager's own unit path, built from the
	// label and the user's home, and is the file this package writes.
	content, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	switch m.GOOS {
	case "darwin":
		return splitCommand(launchdArguments(content))
	case "linux":
		return splitCommand(systemdArguments(string(content)))
	default:
		// Windows registers a scheduled task whose XML this package does not
		// read back. The caller falls back to asking for the value, which is
		// what every platform did before this existed.
		return "", nil
	}
}

func splitCommand(parts []string) (string, []string) {
	if len(parts) == 0 {
		return "", nil
	}
	return parts[0], parts[1:]
}

// launchdArguments reads ProgramArguments out of the plist this package writes.
//
// Walked as a token stream rather than unmarshalled into a struct, because a
// plist dictionary is an untyped sequence of alternating <key> and value
// elements: which array belongs to which key is decided by the order they
// appear in, and a struct decode collects all the keys into one slice and all
// the arrays into another, losing exactly that.
func launchdArguments(content []byte) []string {
	decoder := xml.NewDecoder(bytes.NewReader(content))
	// Set by a <key>ProgramArguments</key>, and cleared by anything else, so
	// only the element that immediately follows that key is read.
	wanted := false
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil
		}
		element, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch element.Name.Local {
		case "key":
			var key string
			if err := decoder.DecodeElement(&key, &element); err != nil {
				return nil
			}
			wanted = key == "ProgramArguments"
		case "array":
			if !wanted {
				continue
			}
			var array struct {
				Strings []string `xml:"string"`
			}
			if err := decoder.DecodeElement(&array, &element); err != nil {
				return nil
			}
			return array.Strings
		default:
			// A value for some other key. The next <key> decides again.
			if wanted {
				wanted = false
			}
		}
	}
}

// systemdArguments reads ExecStart out of the unit this package writes, undoing
// systemdQuote.
func systemdArguments(content string) []string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		rest, found := strings.CutPrefix(line, "ExecStart=")
		if !found {
			continue
		}
		return systemdSplit(rest)
	}
	return nil
}

// systemdSplit is the inverse of systemdQuote: space separated, double quotes
// group, backslash escapes inside them, and %% is a literal %.
//
// Written out rather than approximated with strings.Fields, because the one
// argument that needs it is a path, and a path with a space in it is exactly
// the case where guessing produces a plausible wrong answer — half a database
// path, offered to the owner as the one to keep.
func systemdSplit(line string) []string {
	var parts []string
	var current strings.Builder
	quoted := false
	started := false
	for index := 0; index < len(line); index++ {
		character := line[index]
		switch {
		case character == '"':
			quoted = !quoted
			started = true
		case character == '\\' && index+1 < len(line):
			index++
			current.WriteByte(line[index])
			started = true
		case character == '%' && index+1 < len(line) && line[index+1] == '%':
			index++
			current.WriteByte('%')
			started = true
		case character == '$' && index+1 < len(line) && line[index+1] == '$':
			index++
			current.WriteByte('$')
			started = true
		case (character == ' ' || character == '\t') && !quoted:
			if started {
				parts = append(parts, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteByte(character)
			started = true
		}
	}
	if started {
		parts = append(parts, current.String())
	}
	return parts
}

// ArgumentValue is the value of a named flag in an argument list, in both
// spellings a Go flag accepts: `--db path` and `--db=path`, single dash or
// double.
//
// Empty when the flag is absent, which is a fact worth having: a unit with no
// --db is a node on the default database, and an owner reinstalling it needs to
// be told that rather than shown a blank field.
func ArgumentValue(args []string, name string) string {
	for index, argument := range args {
		trimmed := strings.TrimLeft(argument, "-")
		if trimmed == name {
			if index+1 < len(args) {
				return args[index+1]
			}
			return ""
		}
		if value, found := strings.CutPrefix(trimmed, name+"="); found {
			return value
		}
	}
	return ""
}
