// SPDX-License-Identifier: MIT
// Copyright (c) 2026 neXal contributors. Original conservative triage rules.
// Matches require review; these patterns can also occur in benign tooling.
rule nexal_eicar_test : test_only {
 strings:
  $a = "X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*"
 condition: $a
}
rule nexal_script_download_execute : triage {
 strings:
  $download = /\b(curl|wget)[ \t]+[^\r\n]{1,300}\|[ \t]*(ba)?sh\b/
 condition: filesize < 4194305 and $download
}
rule nexal_powershell_encoded_hidden : triage {
 strings:
  $a = "powershell" nocase
  $b = "-encodedcommand" nocase
  $c = "-windowstyle hidden" nocase
 condition: filesize < 4194305 and all of them
}
rule nexal_python_reverse_shell : triage {
 strings:
  $a = "socket.socket("
  $b = "os.dup2("
  $c = "/bin/sh"
  $d = "subprocess"
 condition: filesize < 4194305 and all of them
}
// A conjunction of Mach-O container bytes and persistence/credential/launch
// strings. Security tooling may legitimately contain these; triage only.
rule nexal_macho_persistence_credentials : triage {
 strings:
  $launch = "Library/LaunchAgents/" ascii
  $schedule = "RunAtLoad" ascii
  $credential = "SecKeychainCopyGenericPassword" ascii
  $execute = "/bin/sh" ascii
 condition:
  (uint32(0) == 0xfeedfacf or uint32(0) == 0xfeedface or
   uint32(0) == 0xcffaedfe or uint32(0) == 0xcefaedfe or
   uint32(0) == 0xbebafeca or uint32(0) == 0xcafebabe) and all of them
}
