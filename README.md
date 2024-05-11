[![Release Workflow](https://github.com/Tensai75/nzbrefresh/actions/workflows/build_and_publish.yml/badge.svg?event=release)](https://github.com/Tensai75/nzbrefresh/actions/workflows/build_and_publish.yml)
[![Latest Release)](https://img.shields.io/github/v/release/Tensai75/nzbrefresh?logo=github)](https://github.com/Tensai75/nzbrefresh/releases/latest)

# NZB Refresh
Proof of concept for a cmd line tool to re-upload articles that are missing from providers with low retention or after takedowns.

The cmd line tool analyses the NZB file specified as argument 1 and checks the availability of the individual articles at all Usenet providers listed in the config.json.
If an article is missing from one or more providers, but is still available from at least one provider, the tool downloads the article from the provider where the article is still available and re-uploads it to the providers where the article is missing.
The tool currently uses the POST command to re-upload the article. It is planned to also use the IHAVE command as the preferred option.
The article will be re-uploaded completely unchanged (same message ID, same subject), except for the date header, which will be updated to the current date.

This is a very early alpha version, intended for initial testing only. It is very slow, with a purely sequential process flow and without any optimisation (parallel processing will be implemented later).

## Installation
1. Download the executable file for your system from the release page.
2. Extract the archive.
3. Edit the `config.json` according to your requirements.

## Running the program
Run the program in a cmd line with the following argument:

`nzbrefresh "[PATHTONZBFILE]"`

- `[PATHTONZBFILE]` = Path to the NZB file you want to check/refresh

## Todos
A lot...

This is a Proof of Concept with the minimum necessary features. 
So there is certainly a lot left to do.

## Version history
### alpha 1
- first public version

## Credits
This software is built using golang ([License](https://go.dev/LICENSE)).
