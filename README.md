用于监控ss服务器状况的小工具。

# 搭建指南

1. 安装golang https://golang.org/doc/install 并设置`GOPATH`
2. 执行 `go get -u -v github.com/ayanamist/ss-monitor`
3. 单独创建一个目录，如`BASEDIR=$HOME/ss-monitor; mkdir $BASEDIR`
4. 改变当面目录 `cd $BASEDIR`
5. 复制并编辑配置文件 `cp $GOPATH/src/github.com/ayanamist/ss-monitor/config-example.yaml $BASEDIR/config.yaml`
6. 创建html模板 `ln -s $GOPATH/src/github.com/ayanamist/ss-monitor/index.htm.tpl $BASEDIR/`
7. 试启动 `$GOPATH/bin/ss-monitor` 看看有没有报错
8. 没有报错的话，配置upstart启动脚本`vim /etc/init/ss-monitor.conf`，***请提前将环境变量替换好***<br>
```
start on runlevel [2345]
stop on runlevel [!2345]

respawn
chdir $BASEDIR/ss-monitor
exec $GOPATH/bin/ss-monitor
```
9. 启动upstart job `start ss-monitor`
10. 提供服务。这里有两种方式提供
    1. 使用ss-monitor自己的http服务，优点是简单安全，不会泄漏`config.yaml`，nginx配置一个反向代理就可以了
    2. 使用渲染好的`index.htm`，优点是可以和一些静态文件服务集成，例如Github Pages

# 说明

- 程序会按照下列顺序选择主目录，将会从选定的主目录中加载配置，并保存数据：
    1. 程序文件所在目录
    2. 执行命令的当前目录（推荐）
- 用csv格式按天保存数据，启动时自动加载最近若干分钟的数据，加载时间通过`oldest_history`进行配置。
- ss url格式支持经过base64转换过的，如蓝云页面二维码中的格式，启动后日志中会有转换提示
- 可以指定监听端口为`127.0.0.1:0`来监听随机端口，日志中会输出实际监听的端口号