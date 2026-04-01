import json
import csv
import sqlite3
import time
import threading
import os
from datetime import datetime, timedelta
from flask import Flask, jsonify, send_file
from flask_cors import CORS
import requests

app = Flask(__name__)
CORS(app)

# Configurações
WIALON_HOST = "https://hst-api.wialon.com"
WIALON_TOKEN = "67eba42da80819eb285dd8794678964707481B6FFB3E065405FE46F889143875DE5B3C3A"
UPDATE_INTERVAL = 30  # segundos
DATA_FILE = 'tracking_data.json'
CSV_FILE = 'tracking_data.csv'

# Cache em memória
tracking_cache = {
    'vehicles': [],
    'positions': {},
    'last_update': None,
    'organizations': []
}

# Sessão do Wialon
wialon_sid = None
last_login = 0
LOGIN_EXPIRY = 3600  # 1 hora

# ==================== FUNÇÕES DE AUTENTICAÇÃO ====================

def wialon_request(svc, params, sid=None):
    """Faz requisição à API do Wialon"""
    url = f"{WIALON_HOST}/wialon/ajax.html"
    payload = {
        "svc": svc,
        "params": json.dumps(params),
        "sid": sid or ""
    }
    
    try:
        response = requests.post(url, data=payload, timeout=30)
        response.raise_for_status()
        data = response.json()
        
        if isinstance(data, dict) and "error" in data:
            if data["error"] == 1:  # Sessão expirada
                print("Sessão expirada, será necessário relogin")
                return None
            print(f"Erro Wialon {data['error']}: {data.get('reason', 'Sem motivo')}")
            return None
        
        return data
    except requests.exceptions.Timeout:
        print("Timeout na requisição Wialon")
        return None
    except Exception as e:
        print(f"Erro na requisição Wialon: {e}")
        return None

def do_login():
    """Faz login e retorna SID"""
    global wialon_sid, last_login
    
    # Verifica se a sessão ainda é válida
    if wialon_sid and (time.time() - last_login) < LOGIN_EXPIRY:
        return wialon_sid
    
    print("Fazendo login no Wialon...")
    data = wialon_request("token/login", {"token": WIALON_TOKEN, "fl": 1})
    
    if data and "eid" in data:
        wialon_sid = data["eid"]
        last_login = time.time()
        print(f"Login realizado com sucesso. SID: {wialon_sid[:10]}...")
        return wialon_sid
    
    print("Falha no login")
    return None

# ==================== COLETA DE DADOS ====================

def get_all_units(sid):
    """Busca todas as unidades"""
    print("Buscando unidades...")
    
    result = wialon_request("core/search_items", {
        "spec": {
            "itemsType": "avl_unit",
            "propName": "sys_name",
            "propValueMask": "*",
            "sortType": "sys_name"
        },
        "force": 1,
        "flags": 4611,  # Flag para obter dados completos da unidade
        "from": 0,
        "to": 0
    }, sid)
    
    if result and "items" in result:
        print(f"Encontradas {len(result['items'])} unidades")
        return result["items"]
    
    print("Nenhuma unidade encontrada")
    return []

def get_vehicle_positions(sid, unit_ids, time_from, time_to):
    """Busca posições de vários veículos"""
    positions = {}
    total = len(unit_ids)
    
    for idx, unit_id in enumerate(unit_ids):
        try:
            print(f"Buscando posição do veículo {idx+1}/{total}...")
            
            result = wialon_request("messages/load_interval", {
                "itemId": unit_id,
                "timeFrom": time_from,
                "timeTo": time_to,
                "flags": 1,
                "flagsMask": 0xFF,
                "loadCount": 1  # Só a última mensagem
            }, sid)
            
            if result and "messages" in result and result["messages"]:
                msg = result["messages"][0]
                if msg.get("pos"):
                    pos_data = msg["pos"]
                    positions[unit_id] = {
                        "lat": pos_data.get("y"),
                        "lng": pos_data.get("x"),
                        "speed": pos_data.get("s", 0),
                        "timestamp": msg.get("t", 0),
                        "course": pos_data.get("c", 0),
                        "altitude": pos_data.get("z", 0),
                        "satellites": pos_data.get("sc", 0)
                    }
                    
                    # Adiciona parâmetros extras se existirem
                    if msg.get("p"):
                        params = msg["p"]
                        positions[unit_id]["ignition"] = params.get("d0", 0)
                        positions[unit_id]["event_code"] = params.get("d3", 0)
            
            # Descarrega mensagens para liberar memória
            wialon_request("messages/unload", {}, sid)
            
            # Pequena pausa para não sobrecarregar
            time.sleep(0.2)
            
        except Exception as e:
            print(f"Erro ao buscar posição do veículo {unit_id}: {e}")
            positions[unit_id] = {}
    
    return positions

def extract_vehicle_info(unit):
    """Extrai informações do veículo do objeto da API"""
    vehicle = {
        "id": unit.get("id"),
        "name": unit.get("nm", f"Veículo {unit.get('id', 'unknown')}"),
        "plate": "",
        "model": "",
        "group_id": None,
        "client_id": None
    }
    
    # Tenta extrair placa e modelo de diferentes fontes
    # 1. Do campo "cls" (se for dicionário)
    cls_data = unit.get("cls")
    if isinstance(cls_data, dict):
        vehicle["plate"] = cls_data.get("plate", "")
        vehicle["model"] = cls_data.get("model", cls_data.get("name", ""))
    elif isinstance(cls_data, str):
        vehicle["plate"] = cls_data
    
    # 2. Do campo "label" (pode conter placa)
    if not vehicle["plate"] and unit.get("label"):
        vehicle["plate"] = unit.get("label")
    
    # 3. Do nome (tenta extrair placa)
    if not vehicle["plate"] and vehicle["name"]:
        # Tenta encontrar padrão de placa no nome
        import re
        plate_match = re.search(r'[A-Z]{3}-\d{4}|[A-Z]{3}\d{4}', vehicle["name"])
        if plate_match:
            vehicle["plate"] = plate_match.group()
    
    # Extrai grupo e cliente se disponível
    if unit.get("g"):
        vehicle["group_id"] = unit.get("g")
    
    # Extrai propriedades personalizadas
    props = unit.get("props", {})
    if isinstance(props, dict):
        vehicle["client_id"] = props.get("client_id", props.get("cid"))
    
    return vehicle

def collect_tracking_data():
    """Coleta dados de rastreamento"""
    print(f"\n[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] Coletando dados de rastreamento...")
    
    # Verifica login
    sid = do_login()
    if not sid:
        print("Erro ao fazer login no Wialon")
        return
    
    # Busca unidades
    units = get_all_units(sid)
    if not units:
        print("Nenhuma unidade encontrada")
        return
    
    # Calcula período (últimos 30 dias para histórico, mas só última posição para tracking)
    now = int(time.time())
    month_ago = now - (30 * 24 * 3600)
    
    # Extrai IDs dos veículos
    unit_ids = [u.get("id") for u in units if u.get("id")]
    
    # Busca posições atuais
    print(f"Buscando posições para {len(unit_ids)} veículos...")
    positions = get_vehicle_positions(sid, unit_ids, month_ago, now)
    
    # Prepara dados dos veículos
    vehicles = []
    for unit in units:
        unit_id = unit.get("id")
        if not unit_id:
            continue
            
        vehicle_info = extract_vehicle_info(unit)
        pos = positions.get(unit_id, {})
        
        # Calcula status
        last_update = pos.get("timestamp", 0)
        speed = pos.get("speed", 0)
        ignition = pos.get("ignition", 0)
        
        if speed > 0:
            status = "active"
        elif ignition:
            status = "idle"
        elif last_update and (now - last_update) < 300:  # 5 minutos
            status = "idle"
        else:
            status = "off"
        
        vehicle = {
            "id": unit_id,
            "name": vehicle_info["name"],
            "plate": vehicle_info["plate"],
            "model": vehicle_info["model"],
            "status": status,
            "speed": speed,
            "maxSpeed": 80,
            "lat": pos.get("lat"),
            "lng": pos.get("lng"),
            "lastUpdate": last_update,
            "ignition": ignition,
            "course": pos.get("course", 0),
            "altitude": pos.get("altitude", 0),
            "satellites": pos.get("satellites", 0),
            "event_code": pos.get("event_code", 0)
        }
        
        vehicles.append(vehicle)
    
    # Atualiza cache
    tracking_cache['vehicles'] = vehicles
    tracking_cache['positions'] = positions
    tracking_cache['last_update'] = now
    
    # Salva em arquivos
    save_to_json()
    save_to_csv()
    
    active = len([v for v in vehicles if v["status"] == "active"])
    idle = len([v for v in vehicles if v["status"] == "idle"])
    off = len([v for v in vehicles if v["status"] == "off"])
    with_position = len([v for v in vehicles if v["lat"] is not None])
    
    print(f"Coleta concluída:")
    print(f"  - Total: {len(vehicles)} veículos")
    print(f"  - Ativos: {active}")
    print(f"  - Parados: {idle}")
    print(f"  - Desligados: {off}")
    print(f"  - Com posição: {with_position}")
    print(f"  - Última atualização: {datetime.fromtimestamp(now).strftime('%H:%M:%S')}")

def save_to_json():
    """Salva dados em arquivo JSON"""
    data = {
        "timestamp": tracking_cache['last_update'],
        "vehicles": tracking_cache['vehicles']
    }
    
    try:
        with open(DATA_FILE, 'w', encoding='utf-8') as f:
            json.dump(data, f, ensure_ascii=False, indent=2)
        print(f"Dados salvos em {DATA_FILE}")
    except Exception as e:
        print(f"Erro ao salvar JSON: {e}")

def save_to_csv():
    """Salva dados em arquivo CSV"""
    if not tracking_cache['vehicles']:
        return
    
    # Prepara cabeçalhos
    fieldnames = ['id', 'name', 'plate', 'model', 'status', 'speed', 'maxSpeed', 
                  'lat', 'lng', 'lastUpdate', 'ignition', 'course', 'altitude', 
                  'satellites', 'event_code']
    
    try:
        with open(CSV_FILE, 'w', newline='', encoding='utf-8') as f:
            writer = csv.DictWriter(f, fieldnames=fieldnames)
            writer.writeheader()
            
            for vehicle in tracking_cache['vehicles']:
                row = vehicle.copy()
                # Converte timestamp para string legível
                if row.get('lastUpdate'):
                    row['lastUpdate'] = datetime.fromtimestamp(row['lastUpdate']).strftime('%Y-%m-%d %H:%M:%S')
                writer.writerow(row)
        
        print(f"Dados salvos em {CSV_FILE}")
    except Exception as e:
        print(f"Erro ao salvar CSV: {e}")

# ==================== THREAD DE ATUALIZAÇÃO ====================

def update_loop():
    """Loop de atualização em background"""
    while True:
        try:
            collect_tracking_data()
        except Exception as e:
            print(f"Erro no loop de atualização: {e}")
            import traceback
            traceback.print_exc()
        
        time.sleep(UPDATE_INTERVAL)

def start_updater():
    """Inicia thread de atualização"""
    thread = threading.Thread(target=update_loop, daemon=True)
    thread.start()
    print(f"Atualizador iniciado (intervalo: {UPDATE_INTERVAL}s)")

# ==================== ENDPOINTS ====================

@app.route('/api/tracking')
def get_tracking():
    """Retorna dados de rastreamento do cache"""
    return jsonify({
        "success": True,
        "timestamp": tracking_cache['last_update'],
        "vehicles": tracking_cache['vehicles']
    })

@app.route('/api/tracking/csv')
def get_tracking_csv():
    """Retorna arquivo CSV com dados de rastreamento"""
    if os.path.exists(CSV_FILE):
        return send_file(CSV_FILE, mimetype='text/csv', as_attachment=True, 
                        download_name=f'tracking_{datetime.now().strftime("%Y%m%d_%H%M%S")}.csv')
    return jsonify({"error": "Arquivo não encontrado"}), 404

@app.route('/api/position/<int:unit_id>')
def get_vehicle_position(unit_id):
    """Retorna posição de um veículo específico"""
    pos = tracking_cache['positions'].get(unit_id)
    if pos:
        return jsonify({"success": True, "position": pos})
    return jsonify({"success": False, "error": "Posição não encontrada"}), 404

@app.route('/api/status')
def get_status():
    """Retorna status do servidor"""
    active = len([v for v in tracking_cache['vehicles'] if v.get("status") == "active"])
    return jsonify({
        "authenticated": tracking_cache['last_update'] is not None,
        "last_update": tracking_cache['last_update'],
        "vehicles_count": len(tracking_cache['vehicles']),
        "active_vehicles": active,
        "update_interval": UPDATE_INTERVAL
    })

@app.route('/api/vehicles')
def get_vehicles():
    """Retorna lista de veículos"""
    return jsonify({"success": True, "vehicles": tracking_cache['vehicles']})

@app.route('/api/refresh')
def refresh_data():
    """Força atualização dos dados"""
    try:
        collect_tracking_data()
        return jsonify({"success": True, "message": "Dados atualizados"})
    except Exception as e:
        return jsonify({"success": False, "error": str(e)}), 500

# ==================== INICIALIZAÇÃO ====================

if __name__ == "__main__":
    print("=" * 60)
    print("  Track7 - Servidor de Rastreamento Otimizado")
    print("=" * 60)
    print(f"  Atualização a cada {UPDATE_INTERVAL} segundos")
    print(f"  Arquivo de dados: {DATA_FILE}")
    print(f"  Arquivo CSV: {CSV_FILE}")
    print("=" * 60)
    
    # Coleta inicial
    collect_tracking_data()
    
    # Inicia atualizador em background
    start_updater()
    
    # Inicia servidor
    print("\nServidor rodando em http://localhost:5000")
    print("Endpoints disponíveis:")
    print("  GET /api/tracking      - Dados de rastreamento em JSON")
    print("  GET /api/tracking/csv  - Dados em CSV")
    print("  GET /api/vehicles      - Lista de veículos")
    print("  GET /api/status        - Status do servidor")
    print("  GET /api/refresh       - Força atualização")
    print("=" * 60)
    
    app.run(host="0.0.0.0", port=5000, debug=False, threaded=True)