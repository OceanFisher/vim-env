## dlv exec

Execute a precompiled binary, and begin a debug session.

### Synopsis


Execute a precompiled binary and begin a debug session.

This command will cause Delve to exec the binary and immediately attach to it to
begin a new debug session. Please note that if the binary was not compiled with
optimizations disabled, it may be difficult to properly debug it. Please
consider compiling debugging binaries with -gcflags="-N -l".

```
dlv exec <path/to/binary>
```

### Options inherited from parent commands

```
      --accept-multiclient   Allows a headless server to accept multiple client connections. Note that the server API is not reentrant and clients will have to coordinate.
      --api-version int      Selects API version when headless. (default 1)
      --backend string       Backend selection:
	default		Uses lldb on macOS, native everywhere else.
	native		Native backend.
	lldb		Uses lldb-server or debugserver.
	rr		Uses mozilla rr (https://github.com/mozilla/rr).
 (default "default")
      --build-flags string   Build flags, to be passed to the compiler.
      --headless             Run debug server only, in headless mode.
      --init string          Init file, executed by the terminal client.
  -l, --listen string        Debugging server listen address. (default "localhost:0")
      --log                  Enable debugging server logging.
      --log-output string    Comma separated list of components that should produce debug output, possible values:
	debugger	Log debugger commands
	gdbwire		Log connection to gdbserial backend
	lldbout		Copy output from debugserver/lldb to standard output
Defaults to "debugger" when logging is enabled with --log.
      --wd string            Working directory for running the program. (default ".")
```

### SEE ALSO
* [dlv](dlv.md)	 - Delve is a debugger for the Go programming language.

