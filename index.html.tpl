<!DOCTYPE html>
<html>
<head>
<title>System Status</title>
<meta charset="UTF-8"/>
<meta http-equiv="X-UA-Compatible" content="IE=10; IE=9; IE=8; IE=7; IE=EDGE"/>
<link rel="stylesheet" type="text/css" href="https://cdnjs.cloudflare.com/ajax/libs/twitter-bootstrap/3.3.6/css/bootstrap.min.css"/>
<style>
.generated-time {
	display: block;
	width: 100%;
	text-align: right;
}
.table thead {
	background-color: white;
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
<script type="text/javascript" src="https://cdnjs.cloudflare.com/ajax/libs/jquery/2.2.4/jquery.min.js"></script>
<script type="text/javascript" src="https://cdnjs.cloudflare.com/ajax/libs/floatthead/1.4.0/jquery.floatThead.min.js"></script>
<script type="text/javascript">
$(function(){$('table.table').floatThead({position: 'fixed'});});
</script>
</head>
<body>
<span class="generated-time">Generated: {{.GeneratedTime}}</span>
<table class="table">
<thead>
<tr>
	<th></th>
	{{- range .Names }}
	<th>{{.}}</th>
	{{- end}}
</tr>
</thead>
<tbody>
{{- range .Rows}}
<tr>
	<td>{{.Time}}</td>
	{{- range .RtList }}
	<td class="rt-{{if lt . 0}}error{{else if eq . 0}}none{{else if isRtSlow .}}slow{{else}}normal{{end}}">
	{{- renderRt . -}}
	</td>
	{{- end}}
</tr>
{{- end}}
</tbody>
</table>
</body>
</html>