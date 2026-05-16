#!/bin/bash
set -e
apt-get update
apt-get install -y gcc-arm-linux-gnueabihf build-essential wget
wget https://chrony-project.org/releases/chrony-4.5.tar.gz
tar xf chrony-4.5.tar.gz
cd chrony-4.5
CC=arm-linux-gnueabihf-gcc \
./configure --prefix=/out \
    --host-machine=arm \
    --host-system=Linux \
    LDFLAGS="-static" \
    --without-nss \
    --without-editline \
    --without-seccomp \
    --without-libcap
make -j$(nproc)
make install
