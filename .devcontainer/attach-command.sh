#!/bin/bash

cp /.gitconfig /home/user/.gitconfig
cp -r /.ssh/ /home/user/
git config --global --add safe.directory /src
