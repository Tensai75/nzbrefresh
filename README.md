[![Release Workflow](https://github.com/Tensai75/nzbrefresh/actions/workflows/build_and_publish.yml/badge.svg?event=release)](https://github.com/Tensai75/nzbrefresh/actions/workflows/build_and_publish.yml)
[![Latest Release)](https://img.shields.io/github/v/release/Tensai75/nzbrefresh?logo=github)](https://github.com/Tensai75/nzbrefresh/releases/latest)

# NZB Refresh
Proof of concept for a cmd line tool to re-upload articles that are missing from providers with low retention or after takedowns.

The cmd line tool analyses the NZB file specified as positional argument and checks the availability of the individual articles at all Usenet providers listed in the provider.json.
If an article is missing from one or more providers, but is still available from at least one provider, the tool downloads the article from the provider where the article is still available and re-uploads it to the providers where the article is missing.
The tool uses the POST command to re-upload the article.
The article will be re-uploaded completely unchanged (same message ID, same subject), except for the date header, which will be updated to the current date.

This is a very early alpha version, intended for initial testing only.

## Installation
1. Download the executable file for your system from the release page.
2. Extract the archive.
3. Edit the `provider.json` according to your requirements.

## Running the program
Run the program in a cmd line with the following argument:

`nzbrefresh [--check] [--provider PROVIDER] [--debug] NZBFILE`

   Positional arguments:
   
     NZBFILE                path to the NZB file to be checked (required)

   Options:
   
     --check, -c            only check availability - don't re-upload (optional)
     
     --provider PROVIDER, -p PROVIDER
                            path to the provider JSON config file (optional / default is: './provider.json')
     
     --debug, -d            logs additional output to log file (optional, log file will be named NZBFILENAME.log)
     
     --help, -h             display this help and exit
     
     --version              display version and exit
     

## Todos
A lot...

This is a Proof of Concept with the minimum necessary features. 
So there is certainly a lot left to do.

## Version history
### alpha 2
- highly improved version with parallel processing

### alpha 1
- first public version

## Credits
This software is built using golang ([License](https://go.dev/LICENSE)).

This software uses the following external libraries:
- github.com/alexflint/go-arg ([License](https://github.com/alexflint/go-arg/blob/master/LICENSE))
- github.com/alexflint/go-scalar ([License](https://github.com/alexflint/go-scalar/blob/master/LICENSE))
- github.com/fatih/color ([License](https://github.com/fatih/color/blob/main/LICENSE.md))
- github.com/mattn/go-colorable ([License](https://github.com/mattn/go-colorable/blob/master/LICENSE))
- github.com/mattn/go-isatty ([License](https://github.com/mattn/go-isatty/blob/master/LICENSE))
- github.com/nu11ptr/cmpb ([License](https://github.com/nu11ptr/cmpb/blob/master/LICENSE))