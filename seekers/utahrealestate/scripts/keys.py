import json

def count_key_occurrences(json_list):

    key_counts = {}
    
    for json_obj in json_list:
        for key, value in json_obj.items():
            if key != "PhName":
                continue
            if value is None:
                continue
            print(value)
            if key in key_counts:
                key_counts[key] += 1
            else:
                key_counts[key] = 1
    
    return key_counts

def get_choices(json_list, key):
    result_set = set()
    for json_obj in json_list:
        if key in json_obj:
            val = json_obj[key]
            result_set.add(val)
    return result_set

def read_json_file(file_path):
    with open(file_path, 'r') as file:
        data = json.load(file)
    return data

# Example usage:
file_path = 'example.json'  # Update with the path to your JSON file

raw_json = read_json_file(file_path)
# key_occurrences = count_key_occurrences(raw_json['data'])
choices = get_choices(raw_json["data"], "proptype_tx")
print(choices)

#{'dom': 121, 'timeclause': 498, 'shortsale': 490, 'listprice_previous': 491, 'dt_sold': 500, 'dt_canceled': 500, 'rating_id': 500, 'client_id': 500, 'rating': 500, 'agent_id': 500, 'visibility': 500, 'branded_url_id': 500, 'nonstandaddress': 468, 'next_openhouse_start': 453, 'next_openhouse': 453, 'op_id': 453, 'openhouse_url': 500, 'openhouse_ts': 453, 'photocount': 2, 'PhName': 2}