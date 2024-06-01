package main

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"os"

	parser "github.com/alexflint/go-arg"
)

// arguments structure
type Args struct {
	NZBFile   string `arg:"positional" help:"path to the NZB file to be checked"`
	CheckOnly bool   `arg:"-c, --check" help:"only check availability - don't re-upload"`
	Provider  string `arg:"-p, --provider" help:"path to the provider JSON config file (Default: './provider.json')"`
	Debug     bool   `arg:"-d, --debug" help:"logs additional output to log file"`
}

// version information
func (Args) Version() string {
	return fmt.Sprintf("%v %v", appName, appVersion)
}

// additional description
func (Args) Epilogue() string {
	return "For more information visit github.com/Tensai75/nzbrefresh\n"
}

// global arguments variable
var args struct {
	Args
}

// parser variable

func parseArguments() {
	var argParser *parser.Parser

	parserConfig := parser.Config{
		IgnoreEnv: true,
	}

	// parse flags
	argParser, _ = parser.NewParser(parserConfig, &args)
	if err := parser.Parse(&args); err != nil {
		if err.Error() == "help requested by user" {
			writeHelp(argParser)
			os.Exit(0)
		} else if err.Error() == "version requested by user" {
			fmt.Println(args.Version())
			os.Exit(0)
		}
		writeUsage(argParser)
		log.Fatal(err)
	}

	checkArguments(argParser)

}

func checkArguments(argParser *parser.Parser) {
	if args.NZBFile == "" {
		writeUsage(argParser)
		exit(fmt.Errorf("no path to NZB file provided"))
	}

	if args.Provider == "" {
		args.Provider = "./provider.json"
	}
}

func writeUsage(parser *parser.Parser) {
	var buf bytes.Buffer
	parser.WriteUsage(&buf)
	scanner := bufio.NewScanner(&buf)
	for scanner.Scan() {
		fmt.Println("   " + scanner.Text())
	}
}

func writeHelp(parser *parser.Parser) {
	var buf bytes.Buffer
	parser.WriteHelp(&buf)
	scanner := bufio.NewScanner(&buf)
	for scanner.Scan() {
		fmt.Println("   " + scanner.Text())
	}
}
