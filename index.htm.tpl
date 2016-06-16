<!DOCTYPE html>
<html>
<head>
<title>System Status</title>
<meta charset="UTF-8"/>
<link rel="stylesheet" type="text/css" href="https://staticfile.qnssl.com/twitter-bootstrap/3.3.6/css/bootstrap.min.css"/>
<style>
.generated-time {
	display: block;
	width: 100%;
	text-align: right;
}
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
</head>
<body>
<span class="generated-time">Generated: {{.GeneratedTime}}</span>
<table class="table">
<tr>
<th></th>
{{range .Names}}<th>{{.}}</th>{{end}}
</tr>
{{range .Rows}}
<tr>
	<td>{{.Time}}</td>
	{{range .RtList}}<td class="rt-{{if lt . 0}}error{{else if eq . 0}}none{{else if isRtSlow .}}slow{{else}}normal{{end}}">{{if eq . 0}}-{{else if lt . 0}}ERROR{{else}}OK{{end}}</td>{{end}}
</tr>
{{end}}
</table>
</body>
</html>