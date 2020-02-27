#!/bin/zsh
docker run --runtime=runsc-kvm --rm -v /home/zty/go-projects/src/gvisor/overflow-test:/test -it ubuntu
