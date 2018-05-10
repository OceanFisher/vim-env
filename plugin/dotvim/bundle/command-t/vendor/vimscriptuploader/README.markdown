vimscriptuploader.rb -- Upload files to http://www.vim.org
==========================================================

EXPERIMENTAL

This ruby script uploads vim plugins to http://www.vim.org\. You have to 
provide a script definition YAML file that has the following fields:

* id: The plugin's script ID on www.vim.org
* version: The plugin version number
* file: The filename that should be uploaded
* message: The version comment

Example YAML file:

    --- 
    id: "123"
    version: "0.01"
    file: /home/moi/.vim/vimballs/foo.vba
    message: |-
      Fixed this
      Done that

vimscriptdef.rb can generate such YAML files from a vimball recipe.

Those values can also be provided on the command line.


Example use
-----------

Load the user name and the password from a YAML file:

    vimscriptuploader.rb --config vim_org.yml myplugin.yml

where vim\_org.yml could look like:

    ---
    username: foo
    password: bar

Provide all arguments on the command line:

    vimscriptuploader.rb --username foo --password bar \\
        --id 123 --version 0.01 --message "Updated this and that" \\
        --file myplugin.vba



Create YAML plugin definitions with vimscriptdef.rb
------------------------------------------------------

The vimscriptdef.rb ruby script can be used to create a YAML plugin 
definition that can be fed to vimscriptuploader.rb.

In order to make this work, your scripts have to comply to the following 
convention:

* At least one file must contain a `GetLatestVimScripts` tagline. If the 
  file is "foo.vim", the line must look somewhat like:

    `" GetLatestVimScripts: 123 0 :AutoInstall: foo.vim`

* At least one file must set a global `loaded_PLUGIN` variable. If the 
  plugin is "bar", the corresponding line must look like:

    `let loaded_bar = VERSION_NUMBER`

  where `VERSION_NUMBER` is an integer that complies with vim's version 
  numbering system (see :help v:version).

* If you use git tags, vimscriptdef.rb will compile the comment version 
  from the commit messages since the latest tag. The following tag 
  formats are supported (e.g. if the version number is 1.02): v102, 102, 
  1.02.

  If you don't use tags, version comments are limited to simple messages 
  if the configuration file defines a field `history_fmt` that must 
  contain one `%s`, which will be filled in with the plugin name, the 
  formatted string will be posted as version comment.

  The MD5 checksum will be added to the version comment.


Dependencies
------------

* ruby 1.8
* www/mechanize
* git (to extract log messages and tags for the YAML script definition)

