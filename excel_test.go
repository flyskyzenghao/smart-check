package main

import (
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestWriteOutputExcelIncludesInterventionSituation(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.xlsx")
	f := excelize.NewFile()
	sheet := f.GetSheetName(0)
	if err := f.SetCellValue(sheet, "A1", "商机id"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellValue(sheet, "A2", "SJ-1"); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveAs(input); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	output, err := WriteOutputExcel(input, dir, []ResultItem{
		{RowNumber: 2, Intervention: "有效营销", InterventionSituation: "键入2条关键信息", Interruption: "否", Scenario: "新装宽带", MatchedRules: "宽带安装地址、宽带类型", Roles: "一线/专员"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resultFile, err := excelize.OpenFile(output)
	if err != nil {
		t.Fatal(err)
	}
	defer resultFile.Close()

	for cell, want := range map[string]string{
		"B1": "批量介入审核结果",
		"C1": "聊天记录",
		"D1": "介入情况",
		"E1": "是否插话",
		"H1": "参与角色",
		"D2": "键入2条关键信息",
	} {
		got, err := resultFile.GetCellValue(sheet, cell)
		if err != nil {
			t.Fatalf("read %s: %v", cell, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", cell, got, want)
		}
	}
}
