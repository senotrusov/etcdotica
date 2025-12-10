#!/usr/bin/env bash

go build

mkdir -p test/testdots

cd test

mkdir -p testdots/foo testdots/bar
chmod 700 testdots/foo
chmod 755 testdots/bar

echo 1 > testdots/foo/aa
chmod 700 testdots/foo/aa

echo 1 > testdots/foo/bb
chmod 644 testdots/foo/bb

../etcdotica

exit

../etcdotica -watch

( umask 077 ; ../etcdotica -watch )
( umask 027 ; ../etcdotica -watch )
( umask 022 ; ../etcdotica -watch )

( sudo ../etcdotica -watch )

# create/remove some files and see what happens
