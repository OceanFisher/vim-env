#!/usr/bin/env ruby
# @Author:      Tom Link (micathom AT gmail com)
# @License:     GPL (see http://www.gnu.org/licenses/gpl.txt)
# @Created:     2010-11-06.
# @Last Change: 2012-04-20.
# @Revision:    110

require 'optparse'
require 'rbconfig'
require 'logger'
require 'fileutils'
require 'yaml'
require 'digest/md5'


class VimScriptDef
    APPNAME = 'vimscriptdef'
    VERSION = '0.1'

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
            config['dir'] = '.'
            config['cmd'] = nil
            config['format'] = 'vba'

            opts = OptionParser.new do |opts|
                opts.banner =  "Usage: #{File.basename($0)} [OPTIONS] [NAME FILES...]"
                opts.separator ' '
                opts.separator 'vimscriptdef is a free software with ABSOLUTELY NO WARRANTY under'
                opts.separator 'the terms of the GNU General Public License version 2 or newer.'
                opts.separator ' '

                opts.on('--dir DIR', String, 'The working directory') do |value|
                    if File.directory?(value)
                        config['dir'] = value
                    else
                        $logger.fatal "Directory not found: #{value}"
                        exit 5
                    end
                end

                opts.on('-a', '--archive FILENAME', String, 'The plugin distribution archive') do |value|
                    config['archive'] = value
                end

                opts.on('-c', '--config YAML', String, 'Config file') do |value|
                    if File.exist?(value)
                        config.merge!(YAML.load_file(value))
                    else
                        $logger.fatal "Configuration file not found: #{value}"
                        exit 5
                    end
                end

                opts.on('--format FORMAT', ['zip', 'vba'], 'Redistribution format') do |value|
                    config['format'] = value
                end

                opts.on('-n', '--name NAME', String, 'The plugin name') do |value|
                    config['name'] = value
                end

                opts.on('-o', '--out FILENAME', String, 'The filename of the YAML output') do |value|
                    config['outfile'] = value
                end

                opts.on('--print-version', 'Print the plugins current version number') do |value|
                    config['cmd'] = :print_version
                end

                opts.on('--print-saved-version', 'Print the plugins last saved version number') do |value|
                    config['cmd'] = :print_saved_version
                end

                opts.on('--recipe FILENAME', String, 'A vimball recipe (implies --archive, --name and --out)') do |value|
                    config['filename'] = value
                end

                opts.separator ' '
                opts.separator 'Other Options:'

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

            if config['filename']
                config['files'] = File.readlines(config['filename']).map do |filename|
                    filename = filename.chomp
                end
                config['name'] = File.basename(config['filename'], '.*')
                config['archive'] ||= File.join(
                    File.dirname(config['filename']),
                    config['name'] + '.' + config['format']
                )
                config['outfile'] ||= File.join(
                    File.dirname(config['filename']),
                    config['name'] + '.yml'
                )
            end
            config['files'] ||= argv

            $logger.debug "config: #{config}"
            $logger.debug "argv: #{argv}"

            ['name', 'files', 'archive'].each do |param|
                unless config[param]
                    $logger.fatal "No #{param} given"
                    exit 5
                end
            end
            unless File.exist?(config['archive'])
                $logger.fatal "Distribution archive does not exist: #{config['archive']}"
                exit 5
            end

            config['opts'] = opts
            return VimScriptDef.new(config)
        end

    end

    # config ... hash
    def initialize(config)
        @config = config
    end

    def process
        m = "process_#{@config['cmd'] || 'default'}"
        if respond_to?(m)
            send(m)
        else
            $logger.fatal "Unknown command: #{@config['cmd']}"
            puts @config['opts']
            exit 5
        end
    end

    def process_print_version
        puts get_version
    end

    def process_print_saved_version
        yaml = @config['outfile']
        if File.exist?(yaml)
            script_def = YAML.load_file(yaml)
            puts script_def['version']
        end
    end

    def process_default
        FileUtils.cd(@config['dir']) do
            output = @config['outfile'] || $stdout
            if output == '-'
                output = $stdout
            end
            if output.is_a?(File) and File.exist?(output)
                script_def = YAML.load_file(output)
            else
                script_def = {}
            end

            script_id = get_id
            if script_id.nil?
                $logger.error "No Script ID found"
            elsif (script_def.has_key?('id') and script_def['id'] != script_id)
                $logger.error "Script ID mismatch: Expected #{script_def['id']} but got #{script_id}"
                return nil
            end
            script_def['id'] = script_id

            script_def['version'] = get_version
            if script_def['version'].nil?
                return nil
            end

            script_def['message'] = ""
            if File.exist?('.git')
                tags = `git tag`.split(/\n/)
                unless tags.empty?
                    tags.sort! do |a, b|
                        if a =~ /^v?(\d+)$/
                            a = $1
                            af = a.to_f / 100
                        else
                            af = a.to_f
                        end
                        if b =~ /^v?(\d+)$/
                            b = $1
                            bf = b.to_f / 100
                        else
                            bf = b.to_f
                        end
                        if af == 0 and bf == 0
                            a <=> b
                        else
                            af <=> bf
                        end
                    end
                    latest_tag = tags.last
                    $logger.debug "git log --oneline #{latest_tag}.."
                    changes = `git log --oneline #{latest_tag}..`
                    unless changes.empty?
                        changes = changes.split(/\n/)
                        changes.map! do |line|
                            line.sub(/^\S+/, '-')
                        end
                        changes.reverse!
                        if @config['ignore_git_messages_rx']
                            ignore_git_messages_rx = Regexp.new(@config['ignore_git_messages_rx'])
                            changes.delete_if {|line| line =~ ignore_git_messages_rx}
                        end
                        unless changes.empty?
                            script_def['message'] = changes.join("\n")
                            script_def['message'] << "\n"
                        end
                    end
                end
            end
            if script_def['message'].empty? and @config.has_key?('history_fmt')
                script_def['message'] = @config['history_fmt'] % @config['name']
                script_def['message'] << "\n"
            end
            arc = File.open(@config['archive'], 'rb') {|io| io.read}
            script_def['message'] << "MD5 checksum: #{Digest::MD5.hexdigest(arc)}"

            script_def['file'] = @config['archive']

            if script_def.values.any? {|v| v.nil?}
                $logger.fatal "Incomplete script definition"
                exit 5
            end

            case output
            when String
                File.open(output, 'w') {|io| YAML.dump(script_def, io)}
            when IO
                YAML.dump(script_def, output)
            else
                $logger.fatal "Internal error: Unsupported output type: #{output}"
                exit 5
            end
        end
    end


    def get_id
        $logger.debug "Get script ID for #{@config['name']}"
        @config['files'].each do |filename|
            $logger.debug "Get script ID in #{filename}"
            File.readlines(filename).each do |line|
                if line.chomp =~ /^" GetLatestVimScripts: (\d+) +\d+ +(:AutoInstall: +)?#{@config['name']}.vim$/
                    id = $1
                    if id and !id.empty? and id.to_i != 0 and id =~ /[1-9]/
                        $logger.debug "#{@config['name']}: Script ID is ##{id}"
                        return id
                    end
                end
            end
        end
        return nil
    end


    def get_version
        $logger.debug "Get version number for #{@config['name']}"
        @config['files'].each do |filename|
            $logger.debug "Get version number in #{filename}"
            File.readlines(filename).each do |line|
                if line.chomp =~ /^let (g:)?loaded_#{@config['name']} = (\d+)$/
                    version = $2.to_i
                    major = version / 100
                    minor = version - major * 100
                    majmin = "%d.%02d" % [major, minor]
                    $logger.debug "Version number is #{majmin}"
                    return majmin
                end
            end
        end
        $logger.error "Cannot find version number: #{@config['recipe']}"
        return nil
    end

end


if __FILE__ == $0
    VimScriptDef.with_args(ARGV).process
end


