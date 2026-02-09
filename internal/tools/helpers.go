package tools

// В этом файле размещаются вспомогательные функции, общие для инструментов.

// ContainsApplyPatchInvocation определяет, есть ли в bash-скрипте реальный запуск
// команды apply_patch (а не просто упоминание в тексте/кавычках/бэктиках).
// Допускается безопасное упоминание в виде `apply_patch`.
func ContainsApplyPatchInvocation(script string) bool {
	inSingle := false
	inDouble := false
	inBack := false
	i := 0
	for i < len(script) {
		c := script[i]
		switch c {
		case '\'':
			if !inDouble && !inBack {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle && !inBack {
				inDouble = !inDouble
			}
		case '`':
			if !inSingle {
				inBack = !inBack
			}
		}
		if !inSingle && !inDouble && !inBack {
			if hasApplyAtCommandStart(script, i) {
				return true
			}
		}
		i++
	}
	return false
}

// hasApplyAtCommandStart проверяет, что в позиции i находится токен "apply_patch"
// как начало команды (начало строки или после одного из разделителей), и далее
// следует конец или допустимый разделитель аргументов/команд.
func hasApplyAtCommandStart(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	if !hasPrefix(s[i:], "apply_patch") {
		return false
	}
	// Ищем предыдущий непустой символ
	prevNonWS := -1
	for j := i - 1; j >= 0; j-- {
		ch := s[j]
		if ch == ' ' || ch == '\t' || ch == '\r' {
			continue
		}
		prevNonWS = j
		break
	}
	atCmdStart := prevNonWS == -1 || s[prevNonWS] == ';' || s[prevNonWS] == '|' || s[prevNonWS] == '&' || s[prevNonWS] == '\n' || s[prevNonWS] == '('
	k := i + len("apply_patch")
	nextOk := k >= len(s)
	if !nextOk && k < len(s) {
		ch := s[k]
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' || ch == '(' || ch == '{' || ch == '|' || ch == ';' || ch == '&' {
			nextOk = true
		}
	}
	return atCmdStart && nextOk
}

// hasPrefix — локальный быстрый префикс без аллокаций.
func hasPrefix(s, p string) bool {
	if len(p) > len(s) {
		return false
	}
	for i := 0; i < len(p); i++ {
		if s[i] != p[i] {
			return false
		}
	}
	return true
}
