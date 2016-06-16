用于监控ss服务器状况的小工具。

请将`config-example.yaml`更名为`config.yaml`后使用。

按照下列顺序选择主目录：

1. 程序文件所在目录
2. 执行命令的当前目录

从主目录中加载配置，并保存数据。

可以直接使用程序本身的http服务，也可以将`index.htm`文件通过其它http server提供服务。

用csv格式按天保存数据，启动时自动加载最近若干分钟的数据，加载时间通过`oldest_history`进行配置。