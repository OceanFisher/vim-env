mv ~/.vim ~/.vim.bak
mkdir -p ~/.vim/
cp -a ./plugin/dotvim/* ~/.vim/

mv ~/.vimrc ~/.vimrc.bak
cat ./plugin/vimrc > ~/.vimrc

cd plugin/vim-go
sh build.sh

echo ""
echo "Please input command: source ~/.bashrc"
