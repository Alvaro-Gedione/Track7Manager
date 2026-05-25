package main

import (
	"fmt"
	"net"
	"time"
)

// BitReader ajuda a ler valores que não estão alinhados em bytes,
// respeitando a lógica de WriteBit do EasyLogic (LSB first).
type BitReader struct {
	data []byte
	pos  uint
}

func (r *BitReader) ReadBits(bits uint) uint64 {
	var res uint64
	for i := uint(0); i < bits; i++ {
		bytePos := r.pos / 8
		bitPos := r.pos % 8
		if bytePos >= uint(len(r.data)) {
			break
		}
		if (r.data[bytePos] & (1 << bitPos)) != 0 {
			res |= (1 << i)
		}
		r.pos++
	}
	return res
}

func main() {
	fmt.Println("=== SERVIDOR GALILEOSKY V3.2 CORRIGIDO ===")
	fmt.Println("Escutando na porta 4040...")

	ln, err := net.Listen("tcp", "0.0.0.0:4040")
	if err != nil {
		fmt.Printf("Erro ao iniciar servidor: %v\n", err)
		return
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go handleConnection(conn)
	}
}

func handleConnection(conn net.Conn) {
	defer conn.Close()
	addr := conn.RemoteAddr().String()
	fmt.Printf("[%s] Dispositivo conectado\n", addr)

	var stream []byte // Buffer que junta fatias do TCP
	buffer := make([]byte, 4096)

	for {
		n, err := conn.Read(buffer)
		if err != nil {
			fmt.Printf("[%s] Dispositivo desconectado (EOF)\n", addr)
			return
		}
		
		// Adiciona o que chegou ao nosso fluxo contínuo
		stream = append(stream, buffer[:n]...)

		// Tenta extrair todos os pacotes completos disponíveis no stream
		for len(stream) > 0 {
			consumed := tryParsePacket(stream)
			if consumed == 0 {
				// Faltam pedaços do pacote (TCP fatiado). Espera o próximo Read.
				break
			}
			// Avança o stream removendo o pacote que já processamos
			stream = stream[consumed:]
		}
	}
}

func tryParsePacket(data []byte) int {
	if len(data) < 1 { return 0 }

	reader := &BitReader{data: data, pos: 0}
	headerID := uint8(reader.ReadBits(3))

	// Função de segurança: verifica se temos bits suficientes no stream
	hasBits := func(bits uint) bool { return uint(len(data))*8 >= bits }

	readIMEI := func() string {
		high := reader.ReadBits(24)
		low := reader.ReadBits(27)
		return fmt.Sprintf("%d%08d", high, low)
	}

	switch headerID {
	case 0: // INICIO_VIAGEM
		if !hasBits(150) { return 0 } // Precisa de 19 bytes
		imei := readIMEI()
		ts := reader.ReadBits(32)
		lon := int32(reader.ReadBits(32))
		lat := int32(reader.ReadBits(32))
		printBox("INICIO VIAGEM", imei, ts, fmt.Sprintf("Lat: %.6f | Lon: %.6f", float64(lat)/1e6, float64(lon)/1e6))
		return 19 

	case 1: // IDENT_MOTORISTA
		if !hasBits(118) { return 0 } // Precisa de 15 bytes
		imei := readIMEI()
		driver := reader.ReadBits(32)
		ts := reader.ReadBits(32)
		printBox("IDENT. MOTORISTA", imei, ts, fmt.Sprintf("Motorista ID: %d", driver))
		return 15

	case 2: // 30_SEGUNDOS
		if !hasBits(159) { return 0 } // Precisa de 20 bytes
		imei := readIMEI()
		valid := reader.ReadBits(1)
		lon := int32(reader.ReadBits(32))
		lat := int32(reader.ReadBits(32))
		speed := reader.ReadBits(8)
		ts := reader.ReadBits(32)
		status := "GPS OK"
		if valid == 0 { status = "GPS Sem Sinal" }
		printBox("POSICAO (30s)", imei, ts, fmt.Sprintf("%s | Vel: %d km/h | Lat: %.6f | Lon: %.6f", status, speed, float64(lat)/1e6, float64(lon)/1e6))
		return 20

	case 5: // MULTI_EVENTOS
		if !hasBits(102) { return 0 } // Garante que podemos ler pelo menos o count
		imei := readIMEI()
		distancia := reader.ReadBits(32)
		count := reader.ReadBits(16)
		
		// Header(102) + Count * (69 bits de dado + 3 bits de padding = 72)
		totalBits := 102 + uint(count)*72
		if !hasBits(totalBits) { return 0 } // Aguarda resto do pacote chegar

		fmt.Printf("\n>>> [%s] MULTI-EVENTOS Recv: %d | Hodometro: %d m\n", imei, count, distancia)
		for i := 0; i < int(count); i++ {
			id := reader.ReadBits(7)
			dur := reader.ReadBits(17)
			peak := reader.ReadBits(13)
			ts := reader.ReadBits(32)
			
			// PULA O LIXO GERADO PELO FILEWRITE DO GALILEO! (3 bits de padding)
			reader.ReadBits(3)
			
			fmt.Printf("  Event #%d | %-18s | Dur: %ds | Pico: %d | %s\n", 
				i+1, mapEvento(uint8(id)), dur, peak, time.Unix(int64(ts), 0).Format("15:04:05"))
		}
		return int((totalBits + 7) / 8)

	case 6: // MULTI_DADOS
		if !hasBits(78) { return 0 } // Garante que podemos ler pelo menos o count
		imei := readIMEI()
		sensores := reader.ReadBits(8)
		count := reader.ReadBits(16)
		
		// Header(78) + Count * (53 bits de dado + 3 bits de padding = 56)
		totalBits := 78 + uint(count)*56 
		if !hasBits(totalBits) { return 0 } // Aguarda resto do arquivo chegar da rede

		fmt.Printf("\n>>> [%s] MULTI-DADOS Recv: %d registros | Bits Sensores: %08b\n", imei, count, sensores)
		for i := 0; i < int(count); i++ {
			rpm := reader.ReadBits(13)
			speed := reader.ReadBits(8)
			ts := reader.ReadBits(32)
			
			// PULA O LIXO GERADO PELO FILEWRITE DO GALILEO! (3 bits de padding)
			reader.ReadBits(3) 
			
			if i < 3 || i >= int(count)-3 {
				fmt.Printf("  Data #%d | RPM: %4d | Speed: %3d km/h | Time: %s\n", 
					i+1, rpm, speed, time.Unix(int64(ts), 0).Format("15:04:05"))
			} else if i == 3 {
				fmt.Println("  ...")
			}
		}
		return int((totalBits + 7) / 8)

	default:
		// Se o byte não bater com nada, avança 1 byte para o leitor se resincronizar
		fmt.Printf("[DEBUG] Header %d ignorado. Resincronizando... Byte: %02X\n", headerID, data[0])
		return 1 
	}
}

func printBox(title, imei string, ts uint64, extra string) {
	tStr := "Data Invalida"
	if ts > 0 {
		tStr = time.Unix(int64(ts), 0).Format("02/01/2006 15:04:05")
	}
	fmt.Println("\n+------------------------------------------------------------+")
	fmt.Printf("| %-58s |\n", title)
	fmt.Printf("| IMEI: %-52s |\n", imei)
	fmt.Printf("| Hora: %-52s |\n", tStr)
	fmt.Printf("| %-58s |\n", extra)
	fmt.Println("+------------------------------------------------------------+")
}

func mapEvento(id uint8) string {
	events := map[uint8]string{
		101: "Acel. Brusca",
		102: "Freada Brusca",
		103: "Excesso RPM",
		105: "Marcha Lenta",
		106: "Transmissao",
		107: "Curva Brusca",
		108: "Excesso Velocidade",
		109: "RPM Parado",
		110: "Fora Faixa Verde",
	}
	if val, ok := events[id]; ok {
		return val
	}
	return fmt.Sprintf("Evento %d", id)
}