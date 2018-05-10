package config

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/user"
	"path"

	"gopkg.in/yaml.v2"
)

const (
	configDir  string = ".dlv"
	configFile string = "config.yml"
)

// Describes a rule for substitution of path to source code file.
type SubstitutePathRule struct {
	// Directory path will be substituted if it matches `From`.
	From string
	// Path to which substitution is performed.
	To string
}

// Slice of source code path substitution rules.
type SubstitutePathRules []SubstitutePathRule

// Config defines all configuration options available to be set through the config file.
type Config struct {
	// Commands aliases.
	Aliases map[string][]string `yaml:"aliases"`
	// Source code path substitution rules.
	SubstitutePath SubstitutePathRules `yaml:"substitute-path"`

	// MaxStringLen is the maximum string length that the commands print,
	// locals, args and vars should read (in verbose mode).
	MaxStringLen *int `yaml:"max-string-len,omitempty"`
	// MaxArrayValues is the maximum number of array items that the commands
	// print, locals, args and vars should read (in verbose mode).
	MaxArrayValues *int `yaml:"max-array-values,omitempty"`

	// If ShowLocationExpr is true whatis will print the DWARF location
	// expression for its argument.
	ShowLocationExpr bool `yaml:"show-location-expr"`
}

// LoadConfig attempts to populate a Config object from the config.yml file.
func LoadConfig() *Config {
	err := createConfigPath()
	if err != nil {
		fmt.Printf("Could not create config directory: %v.", err)
		return nil
	}
	fullConfigFile, err := GetConfigFilePath(configFile)
	if err != nil {
		fmt.Printf("Unable to get config file path: %v.", err)
		return nil
	}

	f, err := os.Open(fullConfigFile)
	if err != nil {
		f, err = createDefaultConfig(fullConfigFile)
		if err != nil {
			fmt.Printf("Error creating default config file: %v", err)
			return nil
		}
	}
	defer func() {
		err := f.Close()
		if err != nil {
			fmt.Printf("Closing config file failed: %v.", err)
		}
	}()

	data, err := ioutil.ReadAll(f)
	if err != nil {
		fmt.Printf("Unable to read config data: %v.", err)
		return nil
	}

	var c Config
	err = yaml.Unmarshal(data, &c)
	if err != nil {
		fmt.Printf("Unable to decode config file: %v.", err)
		return nil
	}

	return &c
}

func SaveConfig(conf *Config) error {
	fullConfigFile, err := GetConfigFilePath(configFile)
	if err != nil {
		return err
	}

	out, err := yaml.Marshal(*conf)
	if err != nil {
		return err
	}

	f, err := os.Create(fullConfigFile)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(out)
	return err
}

func createDefaultConfig(path string) (*os.File, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("Unable to create config file: %v.", err)
	}
	err = writeDefaultConfig(f)
	if err != nil {
		return nil, fmt.Errorf("Unable to write default configuration: %v.", err)
	}
	return f, nil
}

func writeDefaultConfig(f *os.File) error {
	_, err := f.WriteString(
		`# Configuration file for the delve debugger.

# This is the default configuration file. Available options are provided, but disabled.
# Delete the leading hash mark to enable an item.

# Provided aliases will be added to the default aliases for a given command.
aliases:
  # command: ["alias1", "alias2"]

# Define sources path substitution rules. Can be used to rewrite a source path stored
# in program's debug information, if the sources were moved to a different place
# between compilation and debugging.
# Note that substitution rules will not be used for paths passed to "break" and "trace"
# commands.
substitute-path:
  # - {from: path, to: path}
  
# Maximum number of elements loaded from an array.
# max-array-values: 64

# Maximum loaded string length.
# max-string-len: 64

# Uncomment the following line to make the whatis command also print the DWARF location expression of its argument.
# show-location-expr: true
`)
	return err
}

// createConfigPath creates the directory structure at which all config files are saved.
func createConfigPath() error {
	path, err := GetConfigFilePath("")
	if err != nil {
		return err
	}
	return os.MkdirAll(path, 0700)
}

// GetConfigFilePath gets the full path to the given config file name.
func GetConfigFilePath(file string) (string, error) {
	usr, err := user.Current()
	if err != nil {
		return "", err
	}
	return path.Join(usr.HomeDir, configDir, file), nil
}
