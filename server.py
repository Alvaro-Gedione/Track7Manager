"""
server.py — Servidor Flask integrado com Supabase (VERSÃO CORRIGIDA)
"""

from datetime import datetime, timedelta
import json
import psycopg2
from psycopg2.extras import RealDictCursor
from flask import Flask, request, jsonify, send_from_directory
from flask_cors import CORS
import os
import traceback

app = Flask(__name__)
CORS(app)

# Configurações do Supabase - ATUALIZE COM SUA SENHA REAL
SUPABASE_CONFIG = {
    "host": "aws-1-sa-east-1.pooler.supabase.com",
    "port": 6543,
    "user": "postgres.uoovvwxnerpqeksuogvi",
    "password": "r8xLrSZp7yY%25%23k%2A",  # ⚠️ SUBSTITUA PELA SENHA REAL DO SUPABASE
    "database": "postgres"
}

PROJECT_ROOT = os.path.dirname(os.path.abspath(__file__))

# ============================================
# FUNÇÕES AUXILIARES
# ============================================

def get_db_connection():
    """Retorna conexão com o Supabase com tratamento de erro"""
    try:
        conn = psycopg2.connect(**SUPABASE_CONFIG)
        return conn
    except Exception as e:
        print(f"❌ Erro ao conectar ao Supabase: {e}")
        print(f"   Config: host={SUPABASE_CONFIG['host']}, user={SUPABASE_CONFIG['user']}")
        raise

def datetime_to_str(obj):
    """Converte datetime para string ISO"""
    if isinstance(obj, datetime):
        return obj.isoformat()
    return obj

# ============================================
# ROTAS ESTÁTICAS
# ============================================

@app.route('/')
def index():
    return send_from_directory(PROJECT_ROOT, 'index.html')

@app.route('/<path:filename>')
def static_files(filename):
    return send_from_directory(PROJECT_ROOT, filename)

@app.route('/components/<path:filename>')
def components_files(filename):
    """Serve arquivos da pasta components"""
    components_path = os.path.join(PROJECT_ROOT, 'components')
    return send_from_directory(components_path, filename)

# ============================================
# API ENDPOINTS
# ============================================

@app.route("/api/status", methods=["GET"])
def api_status():
    """Verifica status do sistema e conexão com Supabase"""
    try:
        conn = get_db_connection()
        cur = conn.cursor()
        cur.execute("SELECT 1 as test, NOW() as time")
        result = cur.fetchone()
        cur.close()
        conn.close()
        
        return jsonify({
            "success": True,
            "authenticated": True,
            "database": "connected",
            "timestamp": result[1].isoformat() if result[1] else datetime.now().isoformat(),
            "message": "Sistema operacional"
        })
    except Exception as e:
        print(f"Erro no status: {traceback.format_exc()}")
        return jsonify({
            "success": False,
            "authenticated": False,
            "database": "disconnected",
            "error": str(e)
        }), 500

@app.route("/api/vehicles", methods=["GET"])
def api_vehicles():
    """Retorna todos os veículos com suas últimas posições"""
    try:
        print("🔍 Buscando veículos no Supabase...")
        
        conn = get_db_connection()
        cur = conn.cursor(cursor_factory=RealDictCursor)
        
        # Verifica se a tabela existe e tem dados
        cur.execute("""
            SELECT EXISTS (
                SELECT FROM information_schema.tables 
                WHERE table_name = 'telemetria_live'
            ) as table_exists
        """)
        table_exists = cur.fetchone()['table_exists']
        
        if not table_exists:
            print("❌ Tabela 'telemetria_live' não encontrada!")
            cur.close()
            conn.close()
            return jsonify({
                "success": False, 
                "error": "Tabela 'telemetria_live' não existe no banco de dados"
            }), 500
        
        # Conta total de registros
        cur.execute("SELECT COUNT(*) as total FROM telemetria_live")
        total_records = cur.fetchone()['total']
        print(f"📊 Total de registros na tabela: {total_records}")
        
        if total_records == 0:
            print("⚠️ Nenhum dado encontrado na tabela 'telemetria_live'")
            cur.close()
            conn.close()
            return jsonify({
                "success": True,
                "vehicles": [],
                "message": "Nenhum dado de telemetria encontrado"
            })
        
        # Busca última posição de cada veículo
        query = """
            SELECT DISTINCT ON (imei) 
                imei as id,
                imei as name,
                latitude as lat,
                longitude as lng,
                COALESCE(velocidade, 0) as speed,
                COALESCE(rpm, 0) as rpm,
                timestamp as lastUpdate,
                atributos
            FROM telemetria_live
            WHERE latitude IS NOT NULL 
              AND longitude IS NOT NULL
            ORDER BY imei, timestamp DESC
        """
        
        print(f"📝 Executando query: {query}")
        cur.execute(query)
        vehicles = cur.fetchall()
        
        print(f"✅ Encontrados {len(vehicles)} veículos com posição")
        
        # Converte e limpa dados
        for vehicle in vehicles:
            if vehicle.get('lastUpdate'):
                vehicle['lastUpdate'] = datetime_to_str(vehicle['lastUpdate'])
            vehicle['speed'] = float(vehicle.get('speed') or 0)
            vehicle['rpm'] = int(vehicle.get('rpm') or 0)
            vehicle['is_moving'] = vehicle['speed'] > 0
            vehicle['status'] = "Em movimento" if vehicle['speed'] > 0 else "Parado"
            
            # Converte lat/lng para float
            if vehicle.get('lat'):
                vehicle['lat'] = float(vehicle['lat'])
            if vehicle.get('lng'):
                vehicle['lng'] = float(vehicle['lng'])
            
            if vehicle.get('atributos') and isinstance(vehicle['atributos'], str):
                try:
                    vehicle['atributos'] = json.loads(vehicle['atributos'])
                except:
                    pass
        
        cur.close()
        conn.close()
        
        return jsonify({
            "success": True,
            "vehicles": vehicles,
            "total": len(vehicles),
            "total_records": total_records
        })
        
    except psycopg2.Error as e:
        print(f"❌ Erro PostgreSQL: {e}")
        print(traceback.format_exc())
        return jsonify({
            "success": False, 
            "error": f"Erro no banco de dados: {str(e)}"
        }), 500
    except Exception as e:
        print(f"❌ Erro inesperado em /api/vehicles: {e}")
        print(traceback.format_exc())
        return jsonify({
            "success": False, 
            "error": str(e)
        }), 500

@app.route("/api/vehicles/test", methods=["GET"])
def api_test_vehicles():
    """Endpoint de teste para verificar dados brutos"""
    try:
        conn = get_db_connection()
        cur = conn.cursor(cursor_factory=RealDictCursor)
        
        # Mostra alguns registros brutos para debug
        cur.execute("""
            SELECT imei, latitude, longitude, velocidade, rpm, timestamp
            FROM telemetria_live
            LIMIT 5
        """)
        samples = cur.fetchall()
        
        for sample in samples:
            if sample.get('timestamp'):
                sample['timestamp'] = datetime_to_str(sample['timestamp'])
        
        cur.execute("SELECT COUNT(*) as total FROM telemetria_live")
        total = cur.fetchone()
        
        cur.close()
        conn.close()
        
        return jsonify({
            "success": True,
            "total_records": total['total'],
            "sample_data": samples
        })
        
    except Exception as e:
        print(f"Erro no teste: {e}")
        return jsonify({"success": False, "error": str(e)}), 500

@app.route("/api/alerts", methods=["GET"])
def api_alerts():
    """Retorna alertas ativos"""
    try:
        conn = get_db_connection()
        cur = conn.cursor(cursor_factory=RealDictCursor)
        
        query = """
            SELECT DISTINCT ON (imei) 
                imei as id,
                imei as name,
                latitude as lat,
                longitude as lng,
                velocidade as speed,
                rpm,
                timestamp as lastUpdate,
                CASE 
                    WHEN velocidade > 80 THEN 'Excesso de Velocidade'
                    WHEN rpm > 3500 THEN 'RPM Elevado'
                    ELSE 'Alerta Geral'
                END as alert_type
            FROM telemetria_live
            WHERE (velocidade > 80 OR rpm > 3500)
              AND latitude IS NOT NULL
              AND longitude IS NOT NULL
            ORDER BY imei, timestamp DESC
        """
        cur.execute(query)
        alerts = cur.fetchall()
        
        for alert in alerts:
            if alert.get('lastUpdate'):
                alert['lastUpdate'] = datetime_to_str(alert['lastUpdate'])
            alert['speed'] = float(alert.get('speed') or 0)
            alert['rpm'] = int(alert.get('rpm') or 0)
        
        cur.close()
        conn.close()
        
        return jsonify({
            "success": True,
            "alerts": alerts,
            "total": len(alerts)
        })
        
    except Exception as e:
        print(f"Erro em /api/alerts: {e}")
        return jsonify({"success": False, "error": str(e)}), 500

@app.route("/api/units", methods=["GET"])
def api_units():
    """Retorna lista de IMEIs distintos"""
    try:
        conn = get_db_connection()
        cur = conn.cursor(cursor_factory=RealDictCursor)
        
        query = """
            SELECT DISTINCT 
                imei as id,
                imei as name,
                COUNT(*) as total_readings,
                MIN(timestamp) as first_seen,
                MAX(timestamp) as last_seen
            FROM telemetria_live
            GROUP BY imei
            ORDER BY imei
        """
        cur.execute(query)
        units = cur.fetchall()
        
        for unit in units:
            if unit.get('first_seen'):
                unit['first_seen'] = datetime_to_str(unit['first_seen'])
            if unit.get('last_seen'):
                unit['last_seen'] = datetime_to_str(unit['last_seen'])
        
        cur.close()
        conn.close()
        
        return jsonify({"success": True, "units": units})
        
    except Exception as e:
        print(f"Erro em /api/units: {e}")
        return jsonify({"success": False, "error": str(e)}), 500

# ============================================
# MAIN
# ============================================

if __name__ == "__main__":
    print("=" * 60)
    print("  🚀 SERVIDOR TRACK7 COM SUPABASE")
    print("=" * 60)
    print(f"  📁 Pasta do projeto: {PROJECT_ROOT}")
    print(f"  🗄️  Supabase: {SUPABASE_CONFIG['host']}")
    print(f"  👤 Usuário: {SUPABASE_CONFIG['user']}")
    print("=" * 60)
    
    # Testa conexão com Supabase
    print("\n🔌 Testando conexão com Supabase...")
    try:
        conn = get_db_connection()
        cur = conn.cursor()
        cur.execute("SELECT version(), NOW()")
        version, now = cur.fetchone()
        print(f"✅ Conexão estabelecida!")
        print(f"   Versão PostgreSQL: {version[:50]}...")
        print(f"   Horário do servidor: {now}")
        
        # Verifica tabelas
        cur.execute("""
            SELECT table_name 
            FROM information_schema.tables 
            WHERE table_schema = 'public'
            ORDER BY table_name
        """)
        tables = cur.fetchall()
        print(f"   📊 Tabelas encontradas: {[t[0] for t in tables]}")
        
        # Verifica dados na telemetria_live
        cur.execute("SELECT COUNT(*) FROM telemetria_live")
        count = cur.fetchone()[0]
        print(f"   📈 Registros em telemetria_live: {count}")
        
        cur.close()
        conn.close()
        
        print("\n✨ Servidor pronto para uso!")
        print("=" * 60)
        print("\n📌 Endpoints disponíveis:")
        print("   GET  /api/status              - Status do sistema")
        print("   GET  /api/vehicles            - Posição atual dos veículos")
        print("   GET  /api/vehicles/test       - Teste com dados brutos")
        print("   GET  /api/alerts              - Alertas ativos")
        print("   GET  /api/units               - Lista de IMEIs")
        print("\n🌐 Acesse: http://localhost:5000")
        print("=" * 60)
        
    except Exception as e:
        print(f"❌ ERRO DE CONEXÃO: {e}")
        print("\n⚠️  Verifique:")
        print("   1. A senha do Supabase está correta?")
        print("   2. O host está acessível?")
        print("   3. As configurações de rede permitem conexão?")
        print("\n💡 Dica: Use o endpoint /api/vehicles/test para debug")
        print("=" * 60)
    
    app.run(host="0.0.0.0", port=5000, debug=True)