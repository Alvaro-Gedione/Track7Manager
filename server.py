"""
server.py — Servidor Flask: proxy entre browser e API Wialon.

Inicia em: http://localhost:5000
Serve os arquivos HTML da mesma pasta.

Endpoints:
  GET  /api/status    — status da sessao
  POST /api/login     — login via token
  GET  /api/units     — lista de unidades
  POST /api/messages  — log de mensagens
  POST /api/logout    — encerra a sessao
"""

from datetime import datetime, time
import datetime
import json
import sqlite3
import requests
from flask import Flask, request, jsonify, send_from_directory
from flask_cors import CORS
import os

app = Flask(__name__)
CORS(app)

# Serve a raiz do projeto
PROJECT_ROOT = os.path.dirname(os.path.abspath(__file__))

@app.route('/')
def index():
    return send_from_directory(PROJECT_ROOT, 'index.html')

# Serve qualquer arquivo estático da pasta do projeto
@app.route('/<path:filename>')
def static_files(filename):
    return send_from_directory(PROJECT_ROOT, filename)

@app.route('/api/units')
def get_units_from_db():
    conn = sqlite3.connect('tracking.db')
    conn.row_factory = sqlite3.Row
    cursor = conn.cursor()
    
    # Busca unidades já vinculadas aos seus grupos e clientes
    query = """
        SELECT u.id, u.name, g.name as group_name, c.name as client_name 
        FROM units u
        LEFT JOIN groups g ON u.group_id = g.id
        LEFT JOIN clients c ON u.client_id = c.id
    """
    rows = cursor.execute(query).fetchall()
    return jsonify([dict(row) for row in rows])
# ------------------------------------------------------------------ #
#  CONFIG                                                              #
# ------------------------------------------------------------------ #
WIALON_HOST = "https://hst-api.wialon.com"
PAGES_DIR   = os.path.dirname(os.path.abspath(__file__))

# Token padrao (pode ser sobrescrito via /api/login)
DEFAULT_TOKEN = "67eba42da80819eb285dd8794678964707481B6FFB3E065405FE46F889143875DE5B3C3A"

app = Flask(__name__, static_folder=PAGES_DIR)
CORS(app)

_session = {"sid": None, "token": DEFAULT_TOKEN}

# ------------------------------------------------------------------ #
#  HELPER: chama Wialon e reconecta automaticamente se sessao expirar #
# ------------------------------------------------------------------ #
def _raw_request(svc: str, params: dict, sid: str) -> dict:
    url = f"{WIALON_HOST}/wialon/ajax.html"
    payload = {
        "svc":    svc,
        "params": json.dumps(params),
        "sid":    sid or ""
    }
    resp = requests.post(
        url, data=payload,
        headers={"Content-Type": "application/x-www-form-urlencoded"},
        timeout=60
    )
    resp.raise_for_status()
    data = resp.json()
    return data


def _do_login(token: str) -> str:
    """Faz login e retorna o novo SID."""
    data = _raw_request("token/login", {"token": token, "fl": 1}, sid="")
    if isinstance(data, dict) and "error" in data:
        raise ValueError(f"Falha no login — erro Wialon {data['error']}")
    return data["eid"]


def wialon(svc: str, params: dict) -> dict:
    """
    Executa uma requisicao Wialon.
    Se a sessao estiver invalida (erro 1) faz re-login automatico e tenta de novo.
    """
    # Garante que temos um SID e um token valido
    token = _session.get("token") or DEFAULT_TOKEN
    if not _session.get("sid"):
        _session["sid"] = _do_login(token)

    sid = _session.get("sid") or ""
    data = _raw_request(svc, params, sid)

    # Erro 1 = sessao invalida / expirada → reconecta
    if isinstance(data, dict) and data.get("error") == 1:
        print("[server] Sessao expirada. Reconectando...")
        _session["sid"] = _do_login(token)
        sid = _session.get("sid") or ""
        data = _raw_request(svc, params, sid)

    # Outros erros Wialon (exceto 0 = OK)
    if isinstance(data, dict) and "error" in data and data["error"] != 0:
        raise ValueError(f"Wialon error {data['error']}: {data}")

    return data


# ------------------------------------------------------------------ #
#  ROTAS ESTATICAS                                                     #
# ------------------------------------------------------------------ #
@app.route("/")
def index():
    return send_from_directory(PAGES_DIR, "./pages/list.html")

@app.route("/<path:filename>")
def static_files(filename):
    return send_from_directory(PAGES_DIR, filename)


# ------------------------------------------------------------------ #
#  API                                                                 #
# ------------------------------------------------------------------ #
@app.route("/api/status", methods=["GET"])
def api_status():
    return jsonify({
        "authenticated": bool(_session.get("sid")),
        "wialon_host":   WIALON_HOST
    })


@app.route("/api/login", methods=["POST"])
def api_login():
    body  = request.get_json(silent=True) or {}
    token = body.get("token", "").strip() or DEFAULT_TOKEN
    try:
        _session["token"] = token
        _session["sid"]   = None   # forca re-login
        new_sid = _do_login(token)
        _session["sid"] = new_sid

        # Busca nome do usuario na resposta original
        raw = _raw_request("token/login", {"token": token, "fl": 1}, sid="")
        user_name = raw.get("user", {}).get("nm", "—")

        return jsonify({"ok": True, "user": user_name})
    except Exception as e:
        return jsonify({"error": str(e)}), 401


@app.route("/api/units", methods=["GET"])
def api_units():
    try:
        result = wialon("core/search_items", {
            "spec": {
                "itemsType":     "avl_unit",
                "propName":      "sys_name",
                "propValueMask": "*",
                "sortType":      "sys_name"
            },
            "force": 1,
            "flags": 1,
            "from":  0,
            "to":    0
        })
        units = [{"id": u["id"], "name": u["nm"]} for u in result.get("items", [])]
        return jsonify({"units": units})
    except Exception as e:
        print(f"[server] /api/units erro: {e}")
        return jsonify({"error": str(e)}), 500


@app.route("/api/messages", methods=["POST"])
def api_messages():
    body      = request.get_json(silent=True) or {}
    unit_id   = body.get("unitId")
    time_from = body.get("timeFrom", 0)
    time_to   = body.get("timeTo",   0)
    msg_type  = body.get("msgType",  1)
    max_count = body.get("maxCount", 50)

    if not unit_id:
        return jsonify({"error": "unitId obrigatorio"}), 400

    try:
        result = wialon("messages/load_interval", {
            "itemId":    int(unit_id),
            "timeFrom":  int(time_from),
            "timeTo":    int(time_to),
            "flags":     int(msg_type),
            "flagsMask": 0xFF,
            "loadCount": int(max_count)
        })
        messages = result.get("messages", []) if isinstance(result, dict) else []

        # Libera memoria no servidor Wialon
        try:
            wialon("messages/unload", {})
        except Exception:
            pass

        return jsonify({"messages": messages, "count": len(messages)})
    except Exception as e:
        print(f"[server] /api/messages erro: {e}")
        return jsonify({"error": str(e)}), 500


@app.route("/api/logout", methods=["POST"])
def api_logout():
    try:
        wialon("core/logout", {})
    except Exception:
        pass
    _session["sid"] = None
    return jsonify({"ok": True})
@app.route('/api/route-history', methods=['GET'])
def get_route_history():
    # Inicializa resultados como lista vazia para evitar "not defined"
    resultados = [] 
    
    try:
        imei = request.args.get('imei')
        start_date = request.args.get('start_date')
        end_date = request.args.get('end_date')

        # Agora o datetime.strptime funcionará porque importamos a classe
        if start_date and end_date:
            dt_from = datetime.strptime(start_date, "%Y-%m-%d")
            dt_to = datetime.strptime(end_date, "%Y-%m-%d")
            
            # --- Lógica de busca no Wialon ---
            # resultados = sua_funcao_wialon(imei, dt_from, dt_to)
            # ---------------------------------
        
        return jsonify(resultados)

    except Exception as e:
        print(f"Erro detalhado: {e}")
        return jsonify({"error": str(e)}), 500


def build_trip(positions, trip_id):
    """
    Constrói objeto de viagem a partir das posições.
    Sub-viagens = trechos com speed > 0 (veículo em movimento).
    """
    import math
    from datetime import datetime

    def haversine(lat1, lng1, lat2, lng2):
        R = 6371.0
        phi1, phi2 = math.radians(lat1), math.radians(lat2)
        dphi = math.radians(lat2 - lat1)
        dlam = math.radians(lng2 - lng1)
        a = math.sin(dphi/2)**2 + math.cos(phi1)*math.cos(phi2)*math.sin(dlam/2)**2
        return R * 2 * math.atan2(math.sqrt(a), math.sqrt(1-a))

    def parse_ts(ts):
        for fmt in ('%Y-%m-%d %H:%M:%S', '%Y-%m-%dT%H:%M:%S'):
            try:
                return datetime.strptime(ts, fmt)
            except:
                pass
        return datetime.now()

    start_ts = parse_ts(positions[0]['timestamp'])
    end_ts   = parse_ts(positions[-1]['timestamp'])
    duration_secs = (end_ts - start_ts).total_seconds()
    hours   = int(duration_secs // 3600)
    minutes = int((duration_secs % 3600) // 60)
    seconds = int(duration_secs % 60)

    # Calcula distância e velocidades totais
    total_dist = 0.0
    max_speed  = 0.0
    speed_sum  = 0
    speed_count= 0
    events     = 0
    SPEED_LIMIT = 80  # km/h — ajuste conforme necessário

    for i in range(1, len(positions)):
        p1 = positions[i-1]
        p2 = positions[i]
        if p1.get('lat') and p2.get('lat'):
            total_dist += haversine(p1['lat'], p1['lng'], p2['lat'], p2['lng'])
        spd = p2['speed'] or 0
        if spd > max_speed:
            max_speed = spd
        speed_sum   += spd
        speed_count += 1
        if spd > SPEED_LIMIT:
            events += 1  # conta excesso de velocidade como evento

    avg_speed = round(speed_sum / speed_count, 1) if speed_count else 0

    # ── Detecta sub-viagens (trechos com speed > 0) ────────────────────────
    SPEED_THRESHOLD = 2  # km/h mínimo para considerar "em movimento"
    subtrips = []
    in_subtrip = False
    sub_positions = []

    for pos in positions:
        spd = pos.get('speed') or 0
        if spd > SPEED_THRESHOLD and not in_subtrip:
            in_subtrip = True
            sub_positions = [pos]
        elif spd > SPEED_THRESHOLD and in_subtrip:
            sub_positions.append(pos)
        elif spd <= SPEED_THRESHOLD and in_subtrip:
            # Veículo parou → fecha sub-viagem
            sub_positions.append(pos)
            in_subtrip = False
            if len(sub_positions) >= 2:
                sub = build_subtrip(sub_positions, haversine)
                subtrips.append(sub)
            sub_positions = []

    # Sub-viagem sem fechamento
    if in_subtrip and len(sub_positions) >= 2:
        sub = build_subtrip(sub_positions, haversine)
        subtrips.append(sub)

    stops = len(subtrips) - 1 if len(subtrips) > 1 else 0

    return {
        'id':       trip_id,
        'start':    positions[0]['timestamp'],
        'end':      positions[-1]['timestamp'],
        'duration': f"{hours:02d}:{minutes:02d}:{seconds:02d}",
        'distance': round(total_dist, 1),
        'maxSpeed': round(max_speed, 1),
        'avgSpeed': avg_speed,
        'stops':    stops,
        'events':   events,
        'subtrips': subtrips,
        'rawPositions': [
            {'lat': p['lat'], 'lng': p['lng'],
             'speed': p['speed'], 'timestamp': p['timestamp']}
            for p in positions
            if p.get('lat') and p.get('lng')
        ]
    }

def build_subtrip(positions, haversine_fn):
    """Constrói objeto de sub-viagem (trecho em movimento)."""
    from datetime import datetime

    def parse_ts(ts):
        for fmt in ('%Y-%m-%d %H:%M:%S', '%Y-%m-%dT%H:%M:%S'):
            try:
                return datetime.strptime(ts, fmt)
            except:
                pass
        return datetime.now()

    start_ts = parse_ts(positions[0]['timestamp'])
    end_ts   = parse_ts(positions[-1]['timestamp'])
    dur_secs = (end_ts - start_ts).total_seconds()
    h = int(dur_secs // 3600)
    m = int((dur_secs % 3600) // 60)
    s = int(dur_secs % 60)

    dist = 0.0
    max_spd = 0.0
    for i in range(1, len(positions)):
        p1 = positions[i-1]
        p2 = positions[i]
        if p1.get('lat') and p2.get('lat'):
            dist += haversine_fn(p1['lat'], p1['lng'], p2['lat'], p2['lng'])
        spd = positions[i].get('speed') or 0
        if spd > max_spd:
            max_spd = spd

    return {
        'partida':       positions[0]['timestamp'],
        'chegada':       positions[-1]['timestamp'],
        'duracao':       f"{h:02d}:{m:02d}:{s:02d}",
        'distancia':     round(dist, 1),
        'velocidadeMax': round(max_spd, 1),
        'rawPositions':  [
            {'lat': p['lat'], 'lng': p['lng'],
             'speed': p['speed'], 'timestamp': p['timestamp']}
            for p in positions
            if p.get('lat') and p.get('lng')
        ]
    }

# ------------------------------------------------------------------ #
#  MAIN                                                                #
# ------------------------------------------------------------------ #
if __name__ == "__main__":
    # Login imediato ao iniciar
    try:
        _session["sid"] = _do_login(DEFAULT_TOKEN)
        print("=" * 55)
        print("  Servidor Wialon Proxy")
        print("=" * 55)
        print(f"  Acesse: http://localhost:5000")
        print(f"  Pasta:  {PAGES_DIR}")
        print(f"  Login Wialon: OK (sid={str(_session.get('sid', ''))[:8]}...)")
        print("  Pressione Ctrl+C para encerrar.")
        print("=" * 55)
    except Exception as e:
        print(f"  AVISO: login falhou ao iniciar — {e}")
        print("  O servidor ainda sera iniciado. Use /api/login para autenticar.")

    app.run(host="0.0.0.0", port=5000, debug=True)
