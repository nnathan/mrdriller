# mrdriller

A proof of concept in Go that mimics `wget -r` functionality.

# Build Instructions

`make`

# Examples

## Example 1

Recursively download a subset of files but always ensure up to date MD5 manifest.

```
./mrdriller -refresh MD5 -include 'archlinux/archive/floppy.*' https://mirror.rackspace.com/archlinux/archive/floppy/
```

The following needs a hard include to avoid descending down the `Parent Directory` link on each page of the mirror website.

## Example 2

Mirror a subset of a website:

```
./mrdriller -include '^https://blog.cr.yp.to/?$' -include '2014.*html' -include '.*jpg' -exclude 'saber-fullsize' https://blog.cr.yp.to
```

# Example 3

Mirror a large ISO but allow resume/continue if partially on the filesystem:

```
./mrdriller -depth 5 -resume -refresh 'iso\.' -include '/slackware-iso/?$' -include '/slackware-13.1-iso/?$' -include slackware-13.1-install-d1 'https://mirror.rackspace.com/slackware/slackware-iso/'
```
