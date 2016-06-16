<!DOCTYPE html>
<html>
<header>
<title>System Status</title>
<meta charset="UTF-8"/>
<link rel="stylesheet" type="text/css" href="https://staticfile.qnssl.com/twitter-bootstrap/3.3.6/css/bootstrap.min.css"/>
<style>
.rt-normal {
	background-color: rgba(100,255,100,1);
}
.rt-slow {
	background-color: rgba(255,255,100,1);
}
.rt-error {
	background-color: rgba(255,100,100,0.8);
}
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
	{{range .RtList}}<td class="rt-{{if le . 0}}error{{else if ge . 5000}}slow{{else}}normal{{end}}">{{.}}</td>{{end}}
</tr>
{{end}}
</table>
</body>
</html>