CURRENT_DIR=`pwd`
export GOPATH=$CURRENT_DIR
export GOBIN=$CURRENT_DIR/bin
go install github.com/klauspost/asmfmt/cmd/asmfmt
echo "Building asmfmt"
go install github.com/derekparker/delve/cmd/dlv
echo "Building dlv"
go install github.com/kisielk/errcheck
echo "Building errcheck"
go install github.com/davidrjenni/reftools/cmd/fillstruct
echo "Building fillstruct"
go install github.com/nsf/gocode
echo "Building gocode"
go install github.com/rogpeppe/godef
echo "Building godef"
go install github.com/zmb3/gogetdoc
echo "Building gogetdoc"
go install golang.org/x/tools/cmd/goimports
echo "Building goimports"
go install golang.org/x/lint/golint
echo "Building golint"
go install github.com/alecthomas/gometalinter
echo "Building gometalinter"
go install github.com/fatih/gomodifytags
echo "Building gomodifytags"
go install golang.org/x/tools/cmd/gorename
echo "Building gorename"
go install github.com/jstemmer/gotags
echo "Building gotags"
go install golang.org/x/tools/cmd/guru
echo "Building guru"
go install github.com/josharian/impl
echo "Building impl"
go install github.com/dominikh/go-tools/cmd/keyify
echo "Building keyify"
go install github.com/fatih/motion
echo "Building motion"

echo "export PATH=\$PATH:"${GOBIN} >> ~/.bashrc
