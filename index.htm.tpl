<!DOCTYPE html>
<html>
<header>
<title>System Status</title>
<meta charset="UTF-8"/>
<link rel="stylesheet" type="text/css" href="https://staticfile.qnssl.com/twitter-bootstrap/3.3.6/css/bootstrap.min.css"/>
<style>
</style>
</header>
<body>
<table class="table">
<tr>
<th></th>
{{range .Names}}<th>{{.}}</th>{{end}}
</tr>
{{range .Rows}}
<tr>
	<td>{{.Time}}</td>
	{{range .RtList}}<td>{{.}}</td>{{end}}
</tr>
{{end}}
</table>
</body>
</html>