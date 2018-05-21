# vim-env
将vim打造成编写go的IDE  
整合了vim-go、智能补全、树形目录、gotags、ctrlp快速定位文件、mark标记变量等插件。  

## 目录
*   [1.如何安装插件?](#1.howto-install-vimplugins)
*   [2.如何使用快捷键？](#2.howto-use-shortcut-key)
	*   [2.1.VI IDE 快捷键](#2.1.viide-shortkey)
	*   [2.2.vim-go 快捷键](#2.2.vim-go-shortkey)
*	[3.如何使用nerdtree树形目录](#3.howto-use-nerdtree)
*	[4.Vi快捷键](#4.vi-shortkey)
	*	[4.1.普通命令](#4.1.general-cmd)
	*	[4.2.翻页快捷键](#4.2.page-shortkey)
	*	[4.3.光标移动快捷键](#4.3.cursor)
*	[5.批量注释和反注释](#5.comment)
	*	[5.1.第一种方法](#5.1.firstmethod)
	*	[5.2.第二种方法](#5.2.secondmethod)
*	[6.搜索列表](#6.searchlist)
*	[7.cscope添加go支持](#7.cscope-support-go)


<h2 id="1.howto-install-vimplugins">1.如何安装插件? </h2>

sh install.sh  
这个一键安装脚本在CentOS 7正常运行，其他系统理论上可以用，有一些不兼容：  
neocomplete：智能补全需要vim支持lua插件,查看方法：  
打开vi，输入:version，若显示-lua，则说明没有lua插件，不支持自动补全，但可以手动补全,快捷键为Ctrl + X, Ctrl + O  
gotags：在Mac有点不正常，需要安装linux版本的ctags(Mac使用Homebrew install ctags)  
 
<h2 id="2.howto-use-shortcut-key">2.如何使用快捷键？</h2>
  
<h3 id="2.1.viide-shortkey">2.1.VI IDE 快捷键</h3>

ps:SecureCRT的F7/F8键会失效,可以自己修改快捷键  
  
F7：打开树形目录  

- Ctrl + w + w：在目录和文件切换光标  
- Ctrl + w + l: 向右切换窗口  
- Ctrl + w + h: 向左切换窗口  
  
F8：打开Gotags窗口  

- gd / Ctrl + ] 跳转到声明  
- Ctrl + o / Ctrl + T 返回原来的位置  
- Tab 跳转到历史的下一步(配合Ctrl + o使用)
- Shift + K 打开函数或变量声明，Esc关闭声明窗口
  
neocomplete需要vim支持lua才可以，vim8默认支持  

- Ctrl + x, Ctrl + o：自动补全  

Mark.vim标记

- ml ：高亮  
- md: 删除所有高亮单词  
- mp：跳转到前一个高亮  
- mn：跳转到下一个高亮（目前不可用）  
  
Ctrl + p ：快速定位文件  

<h3 id="2.2.vim-go-shortkey">2.2.vim-go 快捷键</h3>

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

<h2 id="3.howto-use-nerdtree">3.如何使用nerdtree树形目录</h2>

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

<h2 id="4.vi-shortkey">4.Vi快捷键</h2>

<h3 id="4.1.general-cmd">4.1.普通命令</h3>

- :!cmd: 不关闭文件执行shell命令
- Ctrl + G/:f: 显示当前文件名
- :ic 搜索大小写不敏感(ignorecase)
- :noic 搜索大小写敏感
- /\<int\> 全词搜索int

<h3 id="4.2.page-shortkey">4.2.翻页快捷键</h3>

- z回车 将光标所在行移动到屏幕顶端作为预览
- Ctrl+f 往前滚动一整屏 
- Ctrl+b 往后滚动一整屏 
- Ctrl+d 往前滚动半屏 
- Ctrl+u 往后滚动半屏
- Ctrl+e 往后滚动一行 
- Ctrl+y 往前滚动一行

<h3 id="4.3.cursor">4.3.光标移动快捷键</h3>

- h Move left
- j Move down
- k Move up
- l Move right
- w Move to next word
- W Move to next blank delimited word
- b Move to the beginning of the word
- B Move to the beginning of blank delimted word
- e Move to the end of the word
- E Move to the end of Blank delimited word
- ( Move a sentence back
- ) Move a sentence forward
- { Move a paragraph back
- } Move a paragraph forward
- 0 Move to the begining of the line
- $ Move to the end of the line
- 1G Move to the first line of the file
- G Move to the last line of the file
- nG Move to nth line of the file
- :n Move to nth line of the file
- fc Move forward to c
- Fc Move back to c
- H Move to top of screen
- M Move to middle of screen
- L Move to botton of screen
- % Move to associated ( ), { }, [ ]

<h3 id="4.4.moveline">4.4.行操作</h3>

1.剪切操作

```
dd //剪切当前行
p //往下粘贴
P //往上粘贴
```

2.移动操作(剪切操作)

```
23,25 move 27 //将23到25行移动到27行的位置
```

3.复制操作

```
23,30 move 50 //将23到30行复制到50行的位置
```

4.删除操作

```
23,30 del
```

<h2 id="5.comment">5.批量注释和反注释</h2>

<h3 id="5.1.firstmethod">5.1.第一种方法</h3>
  
批量插入字符快捷键：  
Ctrl+v进入VISUAL BLOCK（可视块）模式，按 j （向下选取列）或者 k （向上选取列），再按Shift + i 进入编辑模式然后输入你想要插入的字符（任意字符），再按两次Esc就可以实现批量插入字符，不仅仅实现批量注释而已。  
  
批量删除字符快捷键：  
Ctrl+v进入VISUAL BLOCK（可视块）模式，按 j （向下选取列）或者 k （向上选取列），直接（不用进入编辑模式）按 x 或者 d 就可以直接删去，再按Esc退出。  

<h3 id="5.2.secondmethod">5.2.第二种方法</h3>  
  
批量插入字符快捷键：命令行模式下，输入 " : 首行号，尾行号 s /^/字符/g "实现批量插入字符。如 输入

```
:2,7 s/^/A/g
```
在2到7行首插入A  
  
批量删除字符快捷键：命令行模式下，输入 " : 首行号，尾行号 s /^字符//g "实现批量删除字符。如 输入

```
:2,7 s/^A//g
```
在2到7行首删除A  

<h2 id="6.searchlist">6.搜索列表</h2>

输入

```
:vimgrep /pattern/ %  
```

搜索目录所有文件出现pattern字符串的位置  
  
直接调整到第2个搜索结果  

```
:cc 2  
```
  
打开搜索列表,可用Ctrl+W,W切换窗口  

```
:copen  
```

<h2 id="7.cscope-support-go">7.cscope添加go支持</h2>

目前支持不好  

```
find . -name "\*.go" > cscope.files  
cscope -bkq -i cscope.files  
```
  
打开vi输入

```
:cs add cscope.out  
```
  
快捷键为:

```
Ctrl + \,[c,s,f,t]  
```
  
打开vi输入

```
:cs 
```
可查看帮助  
