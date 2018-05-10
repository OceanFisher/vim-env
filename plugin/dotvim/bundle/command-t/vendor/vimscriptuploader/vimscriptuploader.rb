#!/usr/bin/env ruby
# @Author:      Tom Link (micathom AT gmail com)
# @License:     GPL (see http://www.gnu.org/licenses/gpl.txt)
# @Created:     2010-11-01.
# @Last Change: 2012-04-20.
# @Revision:    63

require 'rubygems'
require 'optparse'
require 'rbconfig'
require 'logger'
require 'yaml'
require 'mechanize'


class VimScriptUploader
    APPNAME = 'vimscriptuploader'
    VERSION = '0.0'

    class AppLog
        def initialize(output=$stdout)
            @output = output
            $logger = Logger.new(output)
            $logger.progname = defined?(APPNAME) ? APPNAME : File.basename($0, '.*')
            $logger.datetime_format = "%H:%M:%S"
            AppLog.set_level
        end
    
        def self.set_level
            if $DEBUG
                $logger.level = Logger::DEBUG
            elsif $VERBOSE
                $logger.level = Logger::INFO
            else
                $logger.level = Logger::WARN
            end
        end
    end


    class << self
    
        def with_args(args)
    
            AppLog.new
    
            config = Hash.new
            opts = OptionParser.new do |opts|
                opts.banner =  "Usage: #{File.basename($0)} [OPTIONS] PLUGIN.yaml ..."
                opts.separator ' '
                opts.separator 'vimscriptuploader is a free software with ABSOLUTELY NO WARRANTY under'
                opts.separator 'the terms of the GNU General Public License version 2 or newer.'
                opts.separator ' '
                opts.separator 'YAML script definitions must have the following fields:'
                opts.separator '  id ....... The plugin\'s script ID on www.vim.org'
                opts.separator '  version .. The plugin version number'
                opts.separator '  message .. The version comment'
                opts.separator '  file ..... The filename that should be uploaded'
                opts.separator ' '

                opts.separator 'General Options:'

                opts.on('-c', '--config YAML', String, 'Config file') do |value|
                    if File.exist?(value)
                        config.merge!(YAML.load_file(value))
                    else
                        $logger.fatal "Configuration file not found: #{value}"
                        exit 5
                    end
                end

                opts.on('--id ID', String, 'The script ID') do |value|
                    config['id'] = value
                end

                opts.on('--file FILENAME', String, 'The plugin') do |value|
                    config['file'] = value
                end

                opts.on('--message TEXT', String, 'The version comment') do |value|
                    config['message'] = value
                end

                opts.on('--message-file FILENAME', String, 'A file containing the version comment') do |value|
                    if File.exist?(value)
                        config['message'] = File.read(value)
                    else
                        $logger.fatal "Message file does not exist: #{value}"
                        exit 5
                    end
                end

                opts.on('--version TEXT', String, 'The script version') do |value|
                    config['version'] = value
                end

                opts.on('--password TEXT', String, 'User password') do |value|
                    config['password'] = value
                end

                opts.on('--username TEXT', String, 'User name') do |value|
                    config['username'] = value
                end

                opts.separator ' '
                opts.separator 'Other Options:'
            
                opts.on('-n', '--[no-]dry-run', 'Don\'t actually run any commands; just print them') do |bool|
                    config['dry'] = bool
                end

                opts.on('--debug', 'Show debug messages') do |v|
                    $DEBUG   = true
                    $VERBOSE = true
                    AppLog.set_level
                end
            
                opts.on('-v', '--verbose', 'Run verbosely') do |v|
                    $VERBOSE = true
                    AppLog.set_level
                end
            
                opts.on_tail('-h', '--help', 'Show this message') do
                    puts opts
                    exit 1
                end
            end
            $logger.debug "command-line arguments: #{args}"
            argv = opts.parse!(args)
            $logger.debug "config: #{config}"
            $logger.debug "argv: #{argv}"

            if argv.empty?
                $logger.fatal "No yaml script definition given"
                exit 5
            end
    
            return VimScriptUploader.new(config, argv)
    
        end
    
    end


    # config ... hash
    # args   ... array of strings
    def initialize(config, args)
        @config = config
        @args   = args
        @logged_in = false
        @agent = ::Mechanize.new do |agent|
            # agent.user_agent = "Mozilla/5.0 (Windows; U; Windows NT 6.1; en-US; rv:1.9.2.4) Gecko/20100513 Firefox/3.6.4"
            agent.user_agent = "#{APPNAME}/#{VERSION}"
        end
    end


    def process
        login
        begin
            @args.each do |yml|
                if File.exist?(yml)
                    script_def = @config.dup
                    script_def.merge!(YAML.load_file(yml))
                    upload(script_def)
                else
                    $logger.error "YAML script definition not found: #{yml}"
                end
            end
        ensure
            logout
        end
    end


    def login
        return if @logged_in
        user = @config['username']
        password = @config['password']
        if !user or !password
            $logger.fatal "Username or password is missing!"
            exit 5
        elsif @config['dry']
            $logger.warn "Login: #{user}:*********"
        else
            @agent.get('http://www.vim.org/login.php') do |page|
                my_page = page.form_with(:name => 'login') do |form|
                    $logger.debug "Form: #{form.inspect}"
                    form.userName = user
                    form.password = password
                end.submit
                $logger.debug "Login result: #{my_page.body}"
                @logged_in = true
            end
        end
    end


    def logout
        if @config['dry']
            $logger.warn "Log out"
        else
            page = @agent.get('http://www.vim.org/logout.php')
            $logger.debug "Logout result: #{page.body}"
        end
        @logged_in = false
    end


    def upload(script_def)
        if script_def['id'].nil? or script_def['id'].empty? or script_def['id'].to_i == 0 
            $logger.fatal "No valid script ID"
            exit 5
        end
        url = 'http://www.vim.org/scripts/add_script_version.php?script_id=%d' % script_def['id']
        $logger.warn "Upload URL: #{url}"
        if @config['dry']
            puts script_def.inspect
        elsif !File.exist?(script_def['file'])
            $logger.error "Plugin file does not exist: #{script_def['file']}"
        else
            @agent.get(url) do |page|
                form = page.form_with(:name => 'script', :method => 'POST')
                form.script_version = script_def['version']
                form.version_comment = script_def['message']
                # form.vim_version = script_def['vim_version']
                # form.field_with(:name => 'vim_version').options[2].select
                form.file_uploads.first.file_name = script_def['file']
                result = form.submit(form.buttons.first)
                $logger.debug "Upload result: #{result.body}"
                return true
            end
        end
        return false
    end

end


if __FILE__ == $0
    VimScriptUploader.with_args(ARGV).process
end

