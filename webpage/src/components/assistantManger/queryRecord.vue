<style lang="less">
@import "../../styles/common.less";
@import "../order/components/table.less";
</style>
<template>
  <div>
    <Row>
      <Card>
        <p slot="title">
          <Icon type="md-person"></Icon>查询审计
        </p>
        <Row>
          <Col span="24">
            <Form inline>
              <FormItem>
                <Input placeholder="账号名" v-model="find.user"></Input>
              </FormItem>
              <FormItem>
                <DatePicker format="yyyy-MM-dd HH:mm" type="datetimerange" placeholder="请选择查询的时间范围"
                            v-model="find.picker" @on-change="find.picker=$event" style="width: 250px"></DatePicker>
              </FormItem>
              <FormItem>
                <Button type="success" @click="queryData">查询</Button>
                <Button type="primary" @click="queryCancel">重置</Button>
              </FormItem>
            </Form>
            <Table border :columns="columns" :data="table_data" stripe size="small"></Table>
          </Col>
        </Row>
        <br>
        <Page :total="page_number" show-elevator @on-change="currentpage" :page-size="20"></Page>
      </Card>
    </Row>
  </div>
</template>
<script>
import axios from 'axios';

export default {
  name: 'put',
  data () {
    return {
      columns: [
        {
          title: '工单编号:',
          key: 'work_id',
          sortable: true
        },
        {
          title: '查询人',
          key: 'username'
        },
        {
          title: '查询人姓名',
          key: 'real_name'
        },
        {
          title: '工单说明',
          key: 'instructions'
        },
        {
          title: '提交时间:',
          key: 'date',
          sortable: true
        },
        {
          title: '操作',
          key: 'action',
          align: 'center',
          render: (h, params) => {
            return h('div', [
              h(
                'Button',
                {
                  props: {
                    size: 'small',
                    type: 'text'
                  },
                  on: {
                    click: () => {
                      this.$router.push({
                        name: 'querylist',
                        query: {
                          workid: params.row.work_id,
                          user: params.row.username
                        }
                      });
                    }
                  }
                },
                '详细信息'
              )
            ]);
          }
        }
      ],
      page_number: 1,
      computer_room: this.$config.computer_room,
      table_data: [],
      find: {
        picker: [],
        user: '',
        valve: false
      }
    };
  },
  methods: {
    currentpage (vl = 1) {
      axios
        .get(`${this.$config.url}/query_worklf?page=${vl}&query=${JSON.stringify(this.find)}`)
        .then(res => {
          [this.table_data, this.page_number] = [res.data.data, res.data.page];
        })
        .catch(error => {
          this.$config.err_notice(this, error);
        });
    },
    queryData () {
      this.find.valve = true
      this.currentpage()
    },
    queryCancel () {
      this.find = this.$config.clearObj(this.find)
      this.currentpage()
    }
  },
  mounted () {
    this.currentpage();
  }
};
</script>
