# vim-env
将vim打造成编写go的IDE  
整合了vim-go、智能补全、树形目录、gotags、ctrlp快速定位文件、mark标记变量等插件。  
  
# 1.How to install vim plugins? 
sh install.sh   
这个一键安装脚本在CentOS 7正常运行，其他希望理论上可以，有一些不兼容：  
neocomplete：智能补全需要vim支持lua插件,查看方法：打开vi，输入:version，若显示-lua，则说明没有lua插件，不支持自动补全，但可以手动补全,快捷键为Ctrl + X, Ctrl + O  
gotags：在Mac有点不正常，需要安装linux版本的ctags  
 
# 2.How to use shortcut key？ 
  
ps:SecureCRT的F7/F8键会失效,可以自己修改快捷键  
  
F7：打开树形目录  
	ctrl + w + w：在目录和文件切换光标  
	ctrl + w + l: 向右切换窗口  
	ctrl + w + h: 向左切换窗口  
  
F8：打开gotags窗口  
	gd 跳转到声明  
	ctrl + o 跳转上一步  
	tab 跳转到历史的下一步
  
neocomplete需要vim支持lua才可以，vim8默认支持  
ctrl + x, ctrl + o：自动补全  
  
ml ：高亮  
md: 删除所有高亮单词  
mp：跳转到前一个高亮  
mn：跳转到下一个高亮（目前不可用）  
  
ctrl + p ：快速定位文件  
  
Go命令：  
- 执行:GoLint，运行golint在当前Go源文件上。  
- 执行:GoDoc，打开当前光标对应符号的Go文档。  
- 执行:GoVet，在当前目录下运行go vet在当前Go源文件上。  
- 执行:GoRun，编译运行当前main package。  
- 执行:GoBuild，编译当前包，这取决于你的源文件，GoBuild不产生结果文件。  
- 执行:GoInstall，安装当前包。  
- 执行:GoTest，测试你当前路径下地\_test.go文件。  
- 执行:GoCoverage，创建一个测试覆盖结果文件，并打开浏览器展示当前包的情况。  
- 执行:GoErrCheck，检查当前包种可能的未捕获的errors。  
- 执行:GoFiles，显示当前包对应的源文件列表。  
- 执行:GoDeps，显示当前包的依赖包列表。  
- 执行:GoImplements，显示当前类型实现的interface列表。  
- 执行:GoRename [to]，将当前光标下的符号替换为[to]。  
- 执行:GoDebug 调试  
- 执行:GoDebugBreakpoint设置断点  
- 还有很多命令，需要在使用的时候再摸索  

# 3.How to use nerdtree mapping key?
只列出比较常用的快捷键，更多快捷键可参考:plugin/dotvim/bundle/nerdtree/doc/NERDTree.txt
- o: 打开文件
- go: 不离开nerdtree焦点打开文件
- i: 上下窗口显示文件
- gi: 不离开nerdtree焦点的i
- s: 左右分隔窗口显示文件
- gs: 不离开nerdtree焦点的
- u: 往上一层目录
- O: 递归展开目录
- X: 递归收缩目录
- P: 到达根目录
- p: 到达当前的根目录
- C: 改变当前目录
- r: 刷新
- R: 递归刷新
- q: 关闭tree窗口

# 4.Vi shortcut key
- :!cmd: 不关闭文件执行shell命令
- Ctrl + G/:f: 显示当前文件名

# 5.批量注释和反注释
第一种方法  
  
批量插入字符快捷键：  
Ctrl+v进入VISUAL BLOCK（可视块）模式，按 j （向下选取列）或者 k （向上选取列），再按Shift + i 进入编辑模式然后输入你想要插入的字符（任意字符），再按两次Esc就可以实现批量插入字符，不仅仅实现批量注释而已。  
  
批量删除字符快捷键：  
Ctrl+v进入VISUAL BLOCK（可视块）模式，按 j （向下选取列）或者 k （向上选取列），直接（不用进入编辑模式）按 x 或者 d 就可以直接删去，再按Esc退出。  
  
第二种方法  
  
批量插入字符快捷键：命令行模式下，输入 " : 首行号，尾行号 s /^/字符/g "实现批量插入字符。如 输入:2,7 s/^/A/g，在2到7行首插入A  
  
批量删除字符快捷键：命令行模式下，输入 " : 首行号，尾行号 s /^字符//g "实现批量删除字符。如 输入:2,7 s/^A//g，在2到7行首删除A  

# 6.搜索列表
输入:vimgrep /pattern/ %  
搜索当前文件所有pattern字符串的位置  
  
直接调整到第2个搜索结果  
:cc 2  
  
打开搜索列表,可用Ctrl+W,W切换窗口  
:copen  

# 7.cscope添加go支持
目前支持不好  
find . -name "\*.go" > cscope.files  
cscope -bkq -i cscope.files  
  
打开vi输入:cs add cscope.out  
  
快捷键为:Ctrl + \,[c,s,f,t]  
  
打开vi输入:cs 查看帮助  
