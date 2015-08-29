apt-get update
apt-get -y install vim golang-go htop git protobuf-compiler

rm -rf ~/pingtest/golang/src/github.com

echo "export GOPATH=~/pingtest/golang" >> ~/.bashrc
echo "export PATH=$PATH:$GOPATH/bin" >> ~/.bashrc
source ~/.bashrc

cd ~/pingtest/golang/src/

go get -u github.com/golang/protobuf/{proto,protoc-gen-go}
protoc --go_out=. ptprotos/*.proto
go install server client

cd ~/pingtest/golang/

./bin/server
