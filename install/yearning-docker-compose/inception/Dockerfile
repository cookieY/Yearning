FROM n3uz/centos7-python36
#Dockerfile https://github.com/n3uz/centos7-python36

RUN yum install cmake ncurses-devel gcc gcc-c++ openssl-devel ncurses-libs wget m4 unzip -y 

RUN cd /usr/local/ && \
    wget http://ftp.gnu.org/gnu/bison/bison-2.5.1.tar.gz && \
    tar zxvf bison-2.5.1.tar.gz && \
    cd bison-2.5.1 && \
    ./configure && \
    make -j2 && make install
	
RUN cd /usr/local/ && \
    wget https://github.com/myide/inception/archive/master.zip && \
    unzip master.zip && \
    cd inception-master/ && \
    sh inception_build.sh builddir linux
	
COPY inc.cnf /etc/inc.cnf

CMD /usr/local/inception-master/builddir/mysql/bin/Inception --defaults-file=/etc/inc.cnf

EXPOSE 6669
