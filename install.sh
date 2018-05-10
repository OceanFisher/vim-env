yum install vim ctags
yum erase vim-minimal

cp ./plugin/mcepl-vim8-epel-7.repo /etc/yum.repos.d/

yum update -y vim*
yum install sudo

echo "alias vi='vim'" >> ~/.bashrc

mv ~/.vim ~/.vim.bak
mkdir -p ~/.vim/
tar jxvf ./plugin/dotvim.tar.bz2 -C ~/.vim/

mv ~/.vimrc ~/.vimrc.bak
cat ./plugin/vimrc > ~/.vimrc

cd plugin
tar jxvf vim-go.tar.bz2
cd vim-go
sh build.sh

echo ""
echo "Please input command: source ~/.bashrc"
