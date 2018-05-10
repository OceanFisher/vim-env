# Installation on Windows

Please use the standard `go get` command to build and install Delve on Windows.

```
go get -u github.com/derekparker/delve/cmd/dlv
```

Also, if not already set, you have to add the %GOPATH%\bin directory to your PATH variable.

Note: If you are using Go 1.5 you must set `GO15VENDOREXPERIMENT=1` before continuing. The `GO15VENDOREXPERIMENT` env var simply opts into the [Go 1.5 Vendor Experiment](https://docs.google.com/document/d/1Bz5-UB7g2uPBdOx-rw5t9MxJwkfpx90cqG9AFL0JAYo/).
