package main

// splitWindowsCommandLine splits a Windows command line into the arguments a
// Go program started with it sees as os.Args.
//
// Windows hands a process one string, and each program splits it by its own
// rules. The node is a Go program, so the rules that decide what its --db was
// are Go's: os.commandLineToArgv and readNextArg in the standard library's
// os/exec_windows.go, copied here. They are not shell32's CommandLineToArgvW
// (which windows.DecomposeCommandLine calls): that one reads argv[0] without
// backslash escapes and follows the post-2008 rule for a doubled quote inside
// quotes, so for some command lines it answers differently from the node's
// own os.Args — and a different answer about --db is a different database.
// Pure string handling, so it is built and tested on every platform.
func splitWindowsCommandLine(cmd string) []string {
	var args []string
	for len(cmd) > 0 {
		if cmd[0] == ' ' || cmd[0] == '\t' {
			cmd = cmd[1:]
			continue
		}
		var arg []byte
		arg, cmd = readNextWindowsArg(cmd)
		args = append(args, string(arg))
	}
	return args
}

// readNextWindowsArg is os.readNextArg: the next argument and what follows it.
func readNextWindowsArg(cmd string) (arg []byte, rest string) {
	var b []byte
	var inquote bool
	var nslash int
	for ; len(cmd) > 0; cmd = cmd[1:] {
		c := cmd[0]
		switch c {
		case ' ', '\t':
			if !inquote {
				return appendBackslashes(b, nslash), cmd[1:]
			}
		case '"':
			b = appendBackslashes(b, nslash/2)
			if nslash%2 == 0 {
				// The "prior to 2008" rule for a doubled quote inside quotes,
				// which is the one Go follows.
				if inquote && len(cmd) > 1 && cmd[1] == '"' {
					b = append(b, c)
					cmd = cmd[1:]
				}
				inquote = !inquote
			} else {
				b = append(b, c)
			}
			nslash = 0
			continue
		case '\\':
			nslash++
			continue
		}
		b = appendBackslashes(b, nslash)
		nslash = 0
		b = append(b, c)
	}
	return appendBackslashes(b, nslash), ""
}

func appendBackslashes(b []byte, n int) []byte {
	for ; n > 0; n-- {
		b = append(b, '\\')
	}
	return b
}
