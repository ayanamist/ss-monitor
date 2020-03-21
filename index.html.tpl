<!DOCTYPE html>
<html>
<head>
<title>System Status</title>
<meta charset="UTF-8"/>
<meta http-equiv="X-UA-Compatible" content="IE=10; IE=9; IE=8; IE=7; IE=EDGE"/>
<link rel="stylesheet" type="text/css" href="https://cdn.jsdelivr.net/npm/bootstrap@4.4.1/dist/css/bootstrap.min.css"/>
<style>
    .navbar .nav {
        width: calc(100% - 12em);
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
<div class="navbar">
    <ul class="nav nav-pills nav-fill" role="tablist">
        {{ range $idx, $group := .Groups -}}
        <li class="nav-item"><a class="nav-link {{if eq $idx 0}}active{{end}}" id="nav-link-{{$idx}}" data-toggle="tab" role="tab" aria-controls="tab{{$idx}}" aria-selected="{{if eq $idx 0}}true{{else}}false{{end}}" href="#tab{{$idx}}">{{$group.Name}}</a></li>
        {{- end }}
    </ul>
    <div class="float-right">Build time: {{.GeneratedTime}}</div>
</div>
<div class="tab-content">
    {{ range $idx, $group := .Groups -}}
    <div class="tab-pane {{if eq $idx 0}}show active{{end}}" id="tab{{$idx}}" role="tabpanel" aria-labelledby="nav-link-{{$idx}}">
        <table class="table table-bordered table-sm table-hover">
            <thead>
                <tr>
                    <th></th>
                    {{- range $group.ServerNames }}
                    <th>{{.}}</th>
                    {{- end}}
                </tr>
            </thead>
            <tbody>
                {{- range $group.Rows -}}
                <tr>
                    <td>{{ .Time }}</td>
                    {{- range .RtList }}
                    <td class="{{if lt . 0}}table-danger{{else if eq . 0}}table-info{{else if isRtSlow .}}table-warning{{else}}table-success{{end}}">
                    {{- renderRt . -}}
                    </td>
                    {{- end}}
                </tr>
                {{- end -}}
            </tbody>
        </table>
    </div>
    {{- end }}
</div>
<script type="text/javascript" src="https://cdn.jsdelivr.net/npm/jquery@2.2.4/dist/jquery.min.js"></script>
<script type="text/javascript" src="https://cdn.jsdelivr.net/npm/floatthead@2.1.2/dist/jquery.floatThead.min.js"></script>
<script type="text/javascript" src="https://cdn.jsdelivr.net/npm/bootstrap@4.4.1/dist/js/bootstrap.bundle.min.js"></script>
<script type="text/javascript">
$(function(){$('table.table').floatThead({position: 'fixed'});});
</script>
</body>
</html>
