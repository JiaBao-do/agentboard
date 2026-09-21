package agentboard_test

import "hash/crc32"

func crcOf(b []byte) uint32 { return crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli)) }
