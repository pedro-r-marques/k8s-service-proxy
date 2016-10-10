function loadServiceTableContents(tableElement, response) {
    var tbody = tableElement.find('tbody');
    tbody.empty();

    $.each(response, function(key, value){
        var row = $('<tr>');
        tbody.append(row);
        row.append($('<td>').append(key));
        var anchor = $('<a>');
        anchor.attr("href", value.Path);
        anchor.append(value.Path);
        row.append($('<td>').append(anchor));
        row.append($('<td>').append(value.Port));
        row.append($('<td>').append(value.Map));
        row.append($('<td>').append(value.Description));
    });
}

function loadServiceTable(tableElement) {
    $.ajax({
        type: "get",
        url: "services",
        dataType: "json",
        success: function(response) {
            loadServiceTableContents(tableElement, response);
        },
        error: function(jqXHR, textStatus, errorThrown) {
            console.log(textStatus, errorThrown);
        }
    });
}

function loadEndpointTableContents(tableElement, response) {
    var tbody = tableElement.find('tbody');
    tbody.empty();

    $.each(response, function(svcIndex, status) {
        $.each(status.Backends, function(podIndex, endpoint) {
            var row = $('<tr>');
            tbody.append(row);
            var path = "/endpoint/" + status.Name + "/" + podIndex.toString();

            var anchor = $('<a>');
            anchor.attr("href", path);
            anchor.append(path);
            row.append($('<td>').append(anchor));

            row.append($('<td>').append(status.Port));
            row.append($('<td>').append(endpoint.PodName));
            row.append($('<td>').append(endpoint.IP));
        });
    });
}

function loadEndpointTable(tableElement) {
    $.ajax({
        type: "get",
        url: "endpoints",
        dataType: "json",
        success: function(response) {
            loadEndpointTableContents(tableElement, response);
        },
        error: function(jqXHR, textStatus, errorThrown) {
            console.log(textStatus, errorThrown);
        }
    });
}