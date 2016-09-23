function loadTableContents(tableElement, response) {
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

function loadTable(tableElement) {
    $.ajax({
        type: "get",
        url: "discovery",
        dataType: "json",
        success: function(response) {
            loadTableContents(tableElement, response);
        },
        error: function(jqXHR, textStatus, errorThrown) {
            console.log(textStatus, errorThrown);
        }
    });
}