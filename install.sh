yum install -y vim ctags cscope
yum erase -y vim-minimal

cp ./plugin/mcepl-vim8-epel-7.repo /etc/yum.repos.d/

yum update -y vim*
yum install -y sudo

echo "alias vi='vim'" >> ~/.bashrc

mv ~/.vim ~/.vim.bak
mkdir -p ~/.vim/
cp -a ./plugin/dotvim/* ~/.vim/

mv ~/.vimrc ~/.vimrc.bak
cat ./plugin/vimrc > ~/.vimrc

cd plugin/vim-go
sh build.sh

echo ""
echo "Please input command: source ~/.bashrc"
