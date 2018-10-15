<!DOCTYPE html>
<html>
<head>
<title>System Status</title>
<meta charset="UTF-8"/>
<meta http-equiv="X-UA-Compatible" content="IE=10; IE=9; IE=8; IE=7; IE=EDGE"/>
<link rel="stylesheet" type="text/css" href="https://cdn.jsdelivr.net/npm/bootstrap@3.3.7/dist/css/bootstrap.min.css"/>
<style>
    .built-time {
        font-size: xx-small;
    }
    .table thead {
        background-color: white;
    }
    .table th:first-child {
        min-width: 3.4em;
    }
    .table th, .table td {
        text-align: center;
    }
</style>
</head>
<body>
<table class="table table-bordered table-condensed table-hover">
<thead>
<tr>
    <th>
        <span class="built-time">Built:<br>{{.GeneratedTime}}</span>
    </th>
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
	<td class="{{if lt . 0}}danger{{else if eq . 0}}info{{else if isRtSlow .}}warning{{else}}success{{end}}">
	{{- renderRt . -}}
	</td>
	{{- end}}
</tr>
{{- end}}
</tbody>
</table>
<script type="text/javascript" src="https://cdn.jsdelivr.net/npm/jquery@2.2.4/dist/jquery.min.js"></script>
<script type="text/javascript" src="https://cdn.jsdelivr.net/npm/floatthead@2.1.2/dist/jquery.floatThead.min.js"></script>
<script type="text/javascript">
$(function(){$('table.table').floatThead({position: 'fixed'});});
</script>
</body>
</html>
